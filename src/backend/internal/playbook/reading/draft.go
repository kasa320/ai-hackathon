package reading

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

const (
	// 仮の判断処理で1節の説明に最低限必要とみなす分数。
	minPresentationMinutes = 5
	// 前回範囲の復習に使う分数の上限。
	maxReviewMinutes = 20
)

type candidate struct {
	id          string
	name        string
	order       int
	remaining   int
	explainable idSet
	consecutive int
}

// DraftPlan は LLM を使わずに規則だけで次の一手を作る（AGENT_MODE=fake 用）。
// 担当できる人がいない節は持ち越し、誰も担当できなければ確認依頼か管理者判断待ちにする。
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
			return coord.Draft{Kind: coord.DraftAsk, AskMemberIDs: ask, Summary: "予定が未定の人に、参加可能な曜日・時間帯と最大参加時間を確認します。"}, nil
		}
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: fmt.Sprintf("%s〜%sに全員が%d分参加できる共通時間がありません。参加可能時間を見直すか、管理者に期間・所要時間の変更を相談してください。欠席者を除いて確定はしません。", s.PeriodStart, s.PeriodEnd, s.DurationMinutes)}, nil
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
		plan, err := decodePlan(cur.Data)
		if err == nil {
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
		if !pd.WillingToPresent || declined.has(id) {
			continue
		}
		c := &candidate{id: id, name: names[id], order: i, remaining: pd.MaxPresentationMinutes, explainable: newIDSet(pd.ExplainableSectionIDs), consecutive: consecutiveSubstituteCount(s, id)}
		// 3回連続の代役になる人は候補から外す。
		if changeKind == coord.ChangeReplan && !original.has(id) && c.consecutive >= maxConsecutiveSubstitutes {
			continue
		}
		cands = append(cands, c)
	}

	assigned := map[string]*candidate{}
	var covered, deferred []string
	for _, sec := range sd.TargetSectionIDs {
		var best *candidate
		for _, c := range cands {
			if !c.explainable.has(sec) || c.remaining < minPresentationMinutes {
				continue
			}
			if best == nil || better(c, best, keep[sec]) {
				best = c
			}
		}
		if best == nil {
			deferred = append(deferred, sec)
			continue
		}
		best.remaining -= minPresentationMinutes
		assigned[sec] = best
		covered = append(covered, sec)
	}

	if len(covered) == 0 {
		asked := newIDSet(append(append([]string{}, s.Case.AskedMemberIDs...), s.Case.WithdrawnMemberIDs...))
		target := newIDSet(sd.TargetSectionIDs)
		var ask []string
		for _, id := range attending {
			if declined.has(id) || asked.has(id) {
				continue
			}
			_, pd := preparationData(s, id)
			for _, sec := range pd.PreparedSectionIDs {
				if target.has(sec) {
					ask = append(ask, id)
					break
				}
			}
		}
		if len(ask) > 0 {
			return coord.Draft{Kind: coord.DraftAsk, AskMemberIDs: ask, Summary: joinNames(ask, names) + "に、今回担当できる範囲と時間を確認します。"}, nil
		}
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: "今回の範囲を担当できる人がいないため、管理者の判断が必要です。"}, nil
	}

	// 分数の配分。担当者の残り時間を仮押さえから戻して計算する。
	for _, c := range assigned {
		c.remaining += minPresentationMinutes
	}
	review := 0
	var reviewSections []string
	if n := len(sd.CompletedSectionIDs); n > 0 {
		review = min(maxReviewMinutes, s.DurationMinutes/3)
		reviewSections = []string{sd.CompletedSectionIDs[n-1]}
	}
	available := s.DurationMinutes - review
	share := max(1, available*2/3/len(covered))

	plan := PlanData{CoveredSectionIDs: covered, DeferredSectionIDs: nonNil(deferred)}
	var parts []string
	if s.ScheduleStatus == coord.ScheduleProposed {
		days := candidateDays(s, 1)
		if len(days) == 0 {
			// 全員が出られる日が期間内に見つからない。多数決で押し切らず、管理者に返す。
			return coord.Draft{Kind: coord.DraftNoFeasible,
				Summary: fmt.Sprintf("%s〜%s の中に、全員が出られる日がありません。期間を広げるか、出られない日を見直してください。", s.PeriodStart, s.PeriodEnd)}, nil
		}
		plan.StartsAt = days[0].Format(time.RFC3339)
		plan.Frequency = describeFrequency(s)
		parts = append(parts, fmt.Sprintf("%sに開催", formatDay(days[0])))
	}
	used := 0
	for _, sec := range covered {
		c := assigned[sec]
		m := max(1, min(c.remaining, share))
		c.remaining -= m
		used += m
		pid := c.id
		plan.Agenda = append(plan.Agenda, AgendaItem{ID: fmt.Sprintf("item_%d", len(plan.Agenda)+1), Activity: ActivityPresentation, SectionIDs: []string{sec}, PresenterMemberID: &pid, Minutes: m})
		parts = append(parts, fmt.Sprintf("%sさんが「%s」を%d分で説明", c.name, titles[sec], m))
	}
	if review > 0 {
		plan.Agenda = append(plan.Agenda, AgendaItem{ID: fmt.Sprintf("item_%d", len(plan.Agenda)+1), Activity: ActivityReview, SectionIDs: reviewSections, Minutes: review})
		parts = append(parts, fmt.Sprintf("前回範囲の復習%d分", review))
	}
	if rest := available - used; rest >= minPresentationMinutes {
		plan.Agenda = append(plan.Agenda, AgendaItem{ID: fmt.Sprintf("item_%d", len(plan.Agenda)+1), Activity: ActivityDiscussion, SectionIDs: append([]string{}, covered...), Minutes: rest})
		parts = append(parts, fmt.Sprintf("議論%d分", rest))
	}
	summary := strings.Join(parts, "、") + "とします。"
	if len(deferred) > 0 {
		var ts []string
		for _, sec := range deferred {
			ts = append(ts, "「"+titles[sec]+"」")
		}
		summary += strings.Join(ts, "・") + "は次回に持ち越します。"
	}

	raw := mustJSON(plan)
	if err := pb.ValidatePlan(ctx, s, coord.Proposal{ChangeKind: changeKind, Data: raw}); err != nil {
		return coord.Draft{Kind: coord.DraftNoFeasible, Summary: "条件を満たす案を作れなかったため、管理者の判断が必要です。"}, nil
	}
	return coord.Draft{Kind: coord.DraftProposal, Plan: raw, Summary: summary}, nil
}

// better は節 sec の担当候補として c が best より望ましいかを返す。
// 現在の担当の維持 → 代役の偏りが小さい人 → 残り時間が長い人 → メンバー順。
func better(c, best *candidate, keeper string) bool {
	if (c.id == keeper) != (best.id == keeper) {
		return c.id == keeper
	}
	if c.consecutive != best.consecutive {
		return c.consecutive < best.consecutive
	}
	if c.remaining != best.remaining {
		return c.remaining > best.remaining
	}
	return c.order < best.order
}

func joinNames(ids []string, names map[string]string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, names[id]+"さん")
	}
	return strings.Join(parts, "・")
}
