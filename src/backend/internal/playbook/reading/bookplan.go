package reading

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

var _ coord.BookPlanner = Playbook{}

// ValidateBookMaterial は書名・ISBN・目次情報・章を、開催回データと同じ規則で検証する。
// 目次情報が空（{} または省略）なら手入力（manual）として扱う。
func (pb Playbook) ValidateBookMaterial(title string, isbn *string, tocSource, sections json.RawMessage) (coord.BookMaterial, error) {
	v := &coord.ValidationError{}
	if t := strings.TrimSpace(string(tocSource)); t == "" || t == "null" || t == "{}" {
		tocSource = json.RawMessage(`{"kind":"manual","urls":[]}`)
	}
	var toc TocSource
	if err := decodeStrict(tocSource, &toc); err != nil {
		v.Add("toc_source", "目次情報の形式が正しくありません")
	}
	var secs []Section
	if err := decodeStrict(sections, &secs); err != nil {
		v.Add("sections", "章の一覧を {id, title} の配列で指定してください")
	}
	if isbn != nil && strings.TrimSpace(*isbn) == "" {
		isbn = nil
	}
	if err := v.Err(); err != nil {
		return coord.BookMaterial{}, err
	}
	ids := make([]string, 0, len(secs))
	for _, s := range secs {
		ids = append(ids, s.ID)
	}
	raw, _ := json.Marshal(SessionData{BookTitle: title, ISBN: isbn, TocSource: toc, Sections: secs, CompletedSectionIDs: []string{}, TargetSectionIDs: ids})
	norm, err := pb.ValidateSessionData(context.Background(), coord.SessionParams{}, raw)
	if err != nil {
		var ve *coord.ValidationError
		if asValidation(err, &ve) {
			out := &coord.ValidationError{}
			for _, f := range ve.Fields {
				switch {
				case strings.HasPrefix(f.Path, "target_section_ids"), strings.HasPrefix(f.Path, "completed_section_ids"):
					// ブック登録では回ごとの範囲をここで選ばない
				case f.Path == "book_title":
					out.Fields = append(out.Fields, coord.FieldError{Path: "title", Message: f.Message})
				default:
					out.Fields = append(out.Fields, f)
				}
			}
			return coord.BookMaterial{}, out.Err()
		}
		return coord.BookMaterial{}, err
	}
	var d SessionData
	if err := json.Unmarshal(norm, &d); err != nil {
		return coord.BookMaterial{}, err
	}
	rawSections, _ := json.Marshal(d.Sections)
	rawToc, _ := json.Marshal(d.TocSource)
	out := coord.BookMaterial{Title: d.BookTitle, ISBN: d.ISBN, TocSource: rawToc, Sections: rawSections}
	for _, s := range d.Sections {
		out.List = append(out.List, coord.BookSection{ID: s.ID, Title: s.Title})
	}
	return out, nil
}

func asValidation(err error, target **coord.ValidationError) bool {
	for err != nil {
		if v, ok := err.(*coord.ValidationError); ok {
			*target = v
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func parseDay(s string) (time.Time, bool) {
	t, err := time.ParseInLocation(dateLayout, s, jst)
	return t, err == nil && t.Format(dateLayout) == s
}

// SplitBook は期間を開催回数で等分して各回の開催目安の期間を決め、全章を登録順に連続した範囲へ分ける。
// 期間の日数・章の数が開催回数に満たなければ、割り当てられない枠が生じるため検証エラーにする。
func (Playbook) SplitBook(sections []coord.BookSection, periodStart, periodEnd string, n int) ([]coord.BookPlanSlot, error) {
	v := &coord.ValidationError{}
	from, okFrom := parseDay(periodStart)
	to, okTo := parseDay(periodEnd)
	if !okFrom {
		v.Add("period_start", "YYYY-MM-DD で指定してください")
	}
	if !okTo {
		v.Add("period_end", "YYYY-MM-DD で指定してください")
	}
	if n < 1 {
		v.Add("planned_session_count", "1以上で指定してください")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days < 1 {
		v.Add("period_end", "全体開始日と同じ日か、それより後の日を指定してください")
	} else if days < n {
		v.Add("period_end", "期間（%d日）が開催回数（%d回）より短いため、各回の開催目安を決められません", days, n)
	}
	if len(sections) < n {
		v.Add("sections", "章の数（%d）が開催回数（%d回）より少ないため、全回に章を割り当てられません", len(sections), n)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	out := make([]coord.BookPlanSlot, n)
	base, extra := len(sections)/n, len(sections)%n
	next := 0
	for i := 0; i < n; i++ {
		startOff, endOff := i*days/n, (i+1)*days/n-1
		count := base
		if i < extra {
			count++
		}
		ids := make([]string, 0, count)
		for _, s := range sections[next : next+count] {
			ids = append(ids, s.ID)
		}
		next += count
		out[i] = coord.BookPlanSlot{
			Sequence:    i + 1,
			PeriodStart: from.AddDate(0, 0, startOff).Format(dateLayout),
			PeriodEnd:   from.AddDate(0, 0, endOff).Format(dateLayout),
			SectionIDs:  ids,
		}
	}
	return out, nil
}

func windowsOverlap(aStart, aEnd, bStart, bEnd string) bool {
	return aStart <= bEnd && bStart <= aEnd
}

// overlapCount はその担当者が別ブックで同じ時期に担当している枠の数。
func overlapCount(in coord.BookPlanInput, memberID, start, end string) int {
	n := 0
	for _, o := range in.Others {
		if o.AssigneeMemberID == memberID && windowsOverlap(start, end, o.PeriodStart, o.PeriodEnd) {
			n++
		}
	}
	return n
}

// DraftBookPlan は担当を、同時進行中の全ブックを合わせた負担が最も少ない人へ順に割り当てる。
// 同じ人を続けて担当にせず、別ブックと日程が重なる人は後回しにし、担当済みの回数が少ない人を優先する。
func (pb Playbook) DraftBookPlan(in coord.BookPlanInput) (coord.BookPlan, error) {
	if len(in.Members) == 0 {
		return coord.BookPlan{}, fmt.Errorf("担当を割り当てられるメンバー（ログイン済みの在籍者）がいません")
	}
	slots, err := pb.SplitBook(in.Sections, in.PeriodStart, in.PeriodEnd, in.SlotCount)
	if err != nil {
		return coord.BookPlan{}, err
	}
	assigned := map[string]int{}
	prev := ""
	for i := range slots {
		best := ""
		better := func(a, b string) bool {
			pa, pb := in.Loads[a].Concurrent+assigned[a], in.Loads[b].Concurrent+assigned[b]
			if pa != pb {
				return pa < pb
			}
			oa, ob := overlapCount(in, a, slots[i].PeriodStart, slots[i].PeriodEnd), overlapCount(in, b, slots[i].PeriodStart, slots[i].PeriodEnd)
			if oa != ob {
				return oa < ob
			}
			return in.Loads[a].Completed < in.Loads[b].Completed
		}
		for _, m := range in.Members {
			if len(in.Members) >= 2 && m.ID == prev {
				continue
			}
			if best == "" || better(m.ID, best) {
				best = m.ID
			}
		}
		slots[i].AssigneeMemberID = best
		assigned[best]++
		prev = best
	}
	return coord.BookPlan{
		Summary: fmt.Sprintf("全%d回・%d章を登録順に割り当て、同じグループで同時進行中のブックの担当を含めた負担が偏らないよう担当を仮に決めました。", len(slots), len(in.Sections)),
		Slots:   slots,
	}, nil
}

// maxProjectedLoad は計画を実行した場合の、担当者ごとの負担（既存の担当を含む）の最大値。
func maxProjectedLoad(in coord.BookPlanInput, p coord.BookPlan) int {
	counts := map[string]int{}
	for _, s := range p.Slots {
		counts[s.AssigneeMemberID]++
	}
	most := 0
	for id, n := range counts {
		if total := in.Loads[id].Concurrent + n; total > most {
			most = total
		}
	}
	return most
}

// ValidateBookPlan はAIが作った計画案を、章・開催目安・担当の3点で検証する。
//   - 章：全章が登録順に、欠落・重複なく、各回1章以上へ割り当てられている。
//   - 開催目安：全体期間内で、回の順に重ならず並んでいる。
//   - 担当：登録済みで在籍・ログイン済みのメンバー。同じ人が連続せず、同時進行中の全ブックを合わせた
//     最大負担が、規則だけで作った計画（DraftBookPlan）を超えない。
func (pb Playbook) ValidateBookPlan(in coord.BookPlanInput, p coord.BookPlan) error {
	v := &coord.ValidationError{}
	if len(p.Slots) != in.SlotCount {
		v.Add("slots", "枠は開催回数（%d）と同じ数にしてください（%d件）", in.SlotCount, len(p.Slots))
		return v.Err()
	}
	from, okFrom := parseDay(in.PeriodStart)
	to, okTo := parseDay(in.PeriodEnd)
	if !okFrom || !okTo {
		return fmt.Errorf("reading: ブックの期間を読めません")
	}
	eligible := map[string]bool{}
	for _, m := range in.Members {
		eligible[m.ID] = true
	}
	var flat []string
	prevEnd, prevAssignee := "", ""
	for i, s := range p.Slots {
		path := fmt.Sprintf("slots[%d]", i)
		if s.Sequence != i+1 {
			v.Add(path+".sequence", "回の番号は1から順に付けてください")
		}
		start, okS := parseDay(s.PeriodStart)
		end, okE := parseDay(s.PeriodEnd)
		switch {
		case !okS || !okE:
			v.Add(path+".period_start", "開催目安の期間は YYYY-MM-DD で指定してください")
		case end.Before(start):
			v.Add(path+".period_end", "終了日は開始日以降にしてください")
		case start.Before(from) || end.After(to):
			v.Add(path+".period_start", "開催目安は全体期間（%s〜%s）の中にしてください", in.PeriodStart, in.PeriodEnd)
		case prevEnd != "" && s.PeriodStart <= prevEnd:
			v.Add(path+".period_start", "開催目安は前の回より後にしてください")
		}
		if okE {
			prevEnd = s.PeriodEnd
		}
		if len(s.SectionIDs) == 0 {
			v.Add(path+".section_ids", "各回に章を1つ以上割り当ててください")
		}
		flat = append(flat, s.SectionIDs...)
		switch {
		case s.AssigneeMemberID == "" || !eligible[s.AssigneeMemberID]:
			v.Add(path+".assignee_member_id", "担当は在籍していてログイン済みのメンバーから選んでください")
		case len(in.Members) >= 2 && s.AssigneeMemberID == prevAssignee:
			v.Add(path+".assignee_member_id", "同じ人を続けて担当にできません")
		}
		prevAssignee = s.AssigneeMemberID
	}
	// 章は登録順のまま、欠落も重複もなく並んでいること。
	if len(flat) != len(in.Sections) {
		v.Add("slots", "全%d章を1回ずつ割り当ててください（割り当て済み：%d件）", len(in.Sections), len(flat))
	} else {
		for i, id := range flat {
			if id != in.Sections[i].ID {
				v.Add("slots", "章は登録順のまま、欠落・重複なく割り当ててください（%d番目は %q のはずです）", i+1, in.Sections[i].ID)
				break
			}
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	if draft, err := pb.DraftBookPlan(in); err == nil {
		if got, limit := maxProjectedLoad(in, p), maxProjectedLoad(in, draft); got > limit {
			v.Add("slots", "同時進行中の全ブックを合わせた担当の負担が偏っています（最大%d件、%d件以下にしてください）", got, limit)
		}
	}
	return v.Err()
}

// replacementCandidates は担当変更の候補になれる人と、その最小の負担を返す。
// 現在の担当者・この枠で断った人は除き、隣の回の担当者も、ほかに人がいる限り除く。
func replacementCandidates(in coord.ReplacementInput) ([]coord.BookPlanMember, int) {
	skip := map[string]bool{in.Current: true}
	for _, id := range in.Excluded {
		skip[id] = true
	}
	var open []coord.BookPlanMember
	for _, m := range in.Members {
		if !skip[m.ID] {
			open = append(open, m)
		}
	}
	var out []coord.BookPlanMember
	for _, m := range open {
		if in.BookAssignees[in.Sequence-1] != m.ID && in.BookAssignees[in.Sequence+1] != m.ID {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		out = open
	}
	lowest := -1
	for _, m := range out {
		if l := in.Loads[m.ID].Concurrent; lowest < 0 || l < lowest {
			lowest = l
		}
	}
	return out, lowest
}

// DraftReplacement は負担が最も少ない人を、担当済みの回数が少ない順に選ぶ。
func (Playbook) DraftReplacement(in coord.ReplacementInput) (string, error) {
	cands, lowest := replacementCandidates(in)
	best := ""
	for _, m := range cands {
		if in.Loads[m.ID].Concurrent != lowest {
			continue
		}
		if best == "" || in.Loads[m.ID].Completed < in.Loads[best].Completed {
			best = m.ID
		}
	}
	if best == "" {
		return "", coord.ErrNoReplacement
	}
	return best, nil
}

// ValidateReplacement は候補が、在籍する別のメンバーで、候補の中で最も負担が少ないことを確認する。
func (Playbook) ValidateReplacement(in coord.ReplacementInput, memberID string) error {
	v := &coord.ValidationError{}
	cands, lowest := replacementCandidates(in)
	ok := false
	for _, m := range cands {
		if m.ID == memberID {
			ok = true
			if in.Loads[m.ID].Concurrent > lowest {
				v.Add("member_id", "同時進行中の全ブックを合わせた負担が最も少ない人から選んでください")
			}
		}
	}
	if !ok {
		v.Add("member_id", "現在の担当者・この回を断った人・在籍していない人は候補にできません")
	}
	return v.Err()
}
