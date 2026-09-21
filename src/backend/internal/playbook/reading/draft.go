package reading

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

const (
	// 仮の判断処理で1人の説明に最低限必要とみなす分数。
	minPresentationMinutes = 5
	// 前回範囲の復習に使う分数の上限。
	maxReviewMinutes = 20
)

type candidate struct {
	id          string
	name        string
	order       int
	past        int // これまでの開催回で担当した回数
	consecutive int
}

// DraftPlan は LLM を使わずに規則だけで次の一手を作る（AGENT_MODE=fake 用）。
// 説明の担当は、参加予定で辞退していない人に、担当した回数が少ない順で範囲をまとまりごとに割り振る。
// 割り振られた本人が引き受けるかを答える。誰にも割り振れなければ管理者判断待ちにする。
func (pb Playbook) DraftPlan(ctx context.Context, s coord.Snapshot) (coord.Draft, error) {
	sd, err := sessionData(s)
	if err != nil {
		return coord.Draft{}, err
	}
	if s.ScheduleStatus == coord.ScheduleConfirmed && s.PeriodStart != "" && !scheduleAllows(s, s.StartsAt) {
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: "確定日時で全員が参加できなくなりました。欠席者を除いて進めず、管理者と日程を再調整してください。確定日時の変更は新しい開催回で登録してください。"}, nil
	}
	if s.ScheduleStatus == coord.ScheduleProposed && len(candidateDays(s, 1)) == 0 {
		var ask []string
		asked := newIDSet(append(append([]string{}, s.Case.AskedMemberIDs...), s.Case.WithdrawnMemberIDs...))
		for _, m := range s.Members {
			p, d := preparationData(s, m.ID)
			if !asked.has(m.ID) && (p == nil || d.Schedule == nil || d.Schedule.Status == "unknown") {
				ask = append(ask, m.ID)
			}
		}
		if len(ask) > 0 {
			return coord.Draft{Kind: coord.DraftAsk, AskMemberIDs: ask, Summary: "予定が未定の人に、参加できる日や時間帯を確認します。"}, nil
		}
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: fmt.Sprintf("%s〜%sに全員が%d分参加できる共通時間がありません。参加できる時間帯を見直すか、管理者に期間・所要時間の変更を相談してください。欠席者を除いて確定はしません。", s.PeriodStart, s.PeriodEnd, s.DurationMinutes)}, nil
	}
	attending := s.Attending()
	if len(attending) == 0 {
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: "参加予定者がいないため、案を作れません。"}, nil
	}
	changeKind := coord.ChangeInitial
	if len(s.ConfirmedPlans) > 0 {
		changeKind = coord.ChangeReplan
	}
	declined := newIDSet(s.Case.DeclinedMemberIDs)
	names := map[string]string{}
	for _, m := range s.Members {
		names[m.ID] = m.DisplayName
	}
	titles := map[string]string{}
	for _, sec := range sd.Sections {
		titles[sec.ID] = sec.Title
	}

	// 現在の確定計画で各節を担当していた人（担当の維持を優先する）。
	keep := map[string]string{}
	var original idSet
	if cur := s.CurrentPlan(); cur != nil {
		if plan, err := decodePlan(cur.Data); err == nil {
			for _, item := range plan.Agenda {
				if item.PresenterMemberID != nil {
					for _, sec := range item.SectionIDs {
						keep[sec] = *item.PresenterMemberID
					}
				}
			}
		}
		var first PlanData
		if p, err := decodePlan(s.ConfirmedPlans[0].Data); err == nil {
			first = p
		}
		original = newIDSet(first.presenters())
	}

	var cands []*candidate
	for i, id := range attending {
		_, pd := preparationData(s, id)
		if pd.DeclinedPresentation || declined.has(id) {
			continue
		}
		c := &candidate{id: id, name: names[id], order: i, past: pastPresentations(s, id), consecutive: consecutiveSubstituteCount(s, id)}
		// 3回連続の代役になる人は候補から外す。
		if changeKind == coord.ChangeReplan && !original.has(id) && c.consecutive >= maxConsecutiveSubstitutes {
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: "説明を割り振れる人がいない（全員が辞退・欠席した）ため、管理者の判断が必要です。"}, nil
	}

	// 分数の配分。前回範囲の復習 → 説明（残りの2/3）→ 議論。
	review := 0
	var reviewSections []string
	if n := len(sd.CompletedSectionIDs); n > 0 {
		review = min(maxReviewMinutes, s.DurationMinutes/3)
		reviewSections = []string{sd.CompletedSectionIDs[n-1]}
	}
	available := s.DurationMinutes - review
	presTotal := available * 2 / 3
	presenters := min(len(cands), len(sd.TargetSectionIDs), presTotal/minPresentationMinutes, maxAgendaItems-2)
	if presenters < 1 {
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: "会の長さが短く、説明の時間を取れないため、管理者の判断が必要です。"}, nil
	}

	plan := PlanData{CoveredSectionIDs: append([]string{}, sd.TargetSectionIDs...), DeferredSectionIDs: []string{}}
	var parts []string
	if s.ScheduleStatus == coord.ScheduleProposed {
		days := candidateDays(s, 1)
		plan.StartsAt = days[0].Format(time.RFC3339)
		plan.Frequency = describeFrequency(s)
		parts = append(parts, fmt.Sprintf("%sに開催", formatDay(days[0])))
	}

	// 範囲を順番どおりのまとまりに分け、まとまりごとに1人を割り振る。
	// 今の担当を維持できるまとまりを先に決め、残りを担当の少ない人に割り振る。
	chunks := splitSections(sd.TargetSectionIDs, presenters)
	used := map[string]bool{}
	chosen := make([]*candidate, len(chunks))
	for i, chunk := range chunks {
		if c := keeperOf(cands, used, chunk, keep); c != nil {
			chosen[i] = c
			used[c.id] = true
		}
	}
	for i, chunk := range chunks {
		if chosen[i] == nil {
			chosen[i] = pickPresenter(cands, used, chunk, keep)
			used[chosen[i].id] = true
		}
	}
	share := max(1, presTotal/presenters)
	for i, chunk := range chunks {
		best := chosen[i]
		pid := best.id
		plan.Agenda = append(plan.Agenda, AgendaItem{ID: fmt.Sprintf("item_%d", len(plan.Agenda)+1), Activity: ActivityPresentation, SectionIDs: chunk, PresenterMemberID: &pid, Minutes: share})
		parts = append(parts, fmt.Sprintf("%sさんが%sを%d分で説明", best.name, sectionsLabel(titles, chunk), share))
	}
	if review > 0 {
		plan.Agenda = append(plan.Agenda, AgendaItem{ID: fmt.Sprintf("item_%d", len(plan.Agenda)+1), Activity: ActivityReview, SectionIDs: reviewSections, Minutes: review})
		parts = append(parts, fmt.Sprintf("前回範囲の復習%d分", review))
	}
	if rest := available - share*len(chunks); rest >= minPresentationMinutes {
		plan.Agenda = append(plan.Agenda, AgendaItem{ID: fmt.Sprintf("item_%d", len(plan.Agenda)+1), Activity: ActivityDiscussion, SectionIDs: append([]string{}, sd.TargetSectionIDs...), Minutes: rest})
		parts = append(parts, fmt.Sprintf("議論%d分", rest))
	}
	summary := strings.Join(parts, "、") + "とします。"

	raw := mustJSON(plan)
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: changeKind, Data: raw}); err != nil {
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: "条件を満たす案を作れなかったため、管理者の判断が必要です。"}, nil
	}
	return coord.Draft{Kind: coord.DraftProposal, Plan: raw, Summary: summary}, nil
}

// splitSections は節を順番どおり n 個のまとまりに分ける（前のまとまりほど1つ多い）。
func splitSections(ids []string, n int) [][]string {
	out := make([][]string, 0, n)
	size, extra := len(ids)/n, len(ids)%n
	start := 0
	for i := 0; i < n; i++ {
		end := start + size
		if i < extra {
			end++
		}
		out = append(out, append([]string{}, ids[start:end]...))
		start = end
	}
	return out
}

// keeperOf は、まとまりの節を今担当していて、引き続き割り振れる人を返す（いなければ nil）。
func keeperOf(cands []*candidate, used map[string]bool, chunk []string, keep map[string]string) *candidate {
	counts := map[string]int{}
	for _, sec := range chunk {
		if id, ok := keep[sec]; ok {
			counts[id]++
		}
	}
	var best *candidate
	for _, c := range cands {
		if used[c.id] || counts[c.id] == 0 {
			continue
		}
		if best == nil || counts[c.id] > counts[best.id] {
			best = c
		}
	}
	return best
}

// pickPresenter はまとまりの担当者を選ぶ。まだ割り振っていない人の中から、
// 今の担当の維持 → 担当した回数が少ない人 → 代役の偏りが小さい人 → メンバー順で選ぶ。
func pickPresenter(cands []*candidate, used map[string]bool, chunk []string, keep map[string]string) *candidate {
	keeps := map[string]int{}
	for _, sec := range chunk {
		if id, ok := keep[sec]; ok {
			keeps[id]++
		}
	}
	var best *candidate
	for _, c := range cands {
		if used[c.id] {
			continue
		}
		if best == nil || better(c, best, keeps) {
			best = c
		}
	}
	return best
}

func better(c, best *candidate, keeps map[string]int) bool {
	if keeps[c.id] != keeps[best.id] {
		return keeps[c.id] > keeps[best.id]
	}
	if c.past != best.past {
		return c.past < best.past
	}
	if c.consecutive != best.consecutive {
		return c.consecutive < best.consecutive
	}
	return c.order < best.order
}

// pastPresentations は、これまでの開催回（最終の確定計画）で説明を担当した回数。
func pastPresentations(s coord.Snapshot, memberID string) int {
	n := 0
	for _, ps := range s.History {
		if len(ps.ConfirmedPlans) == 0 {
			continue
		}
		var p PlanData
		if json.Unmarshal(ps.ConfirmedPlans[len(ps.ConfirmedPlans)-1].Data, &p) != nil {
			continue
		}
		for _, id := range p.presenters() {
			if id == memberID {
				n++
				break
			}
		}
	}
	return n
}

func joinNames(ids []string, names map[string]string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, names[id]+"さん")
	}
	return strings.Join(parts, "・")
}
