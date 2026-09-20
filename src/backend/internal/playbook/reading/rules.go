package reading

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
)

// ValidISBN13 はハイフンなし13桁でチェックディジットが正しいかを返す。
func ValidISBN13(s string) bool {
	if len(s) != 13 {
		return false
	}
	sum := 0
	for i := 0; i < 13; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if i == 12 {
			return (10-sum%10)%10 == d
		}
		if i%2 == 0 {
			sum += d
		} else {
			sum += 3 * d
		}
	}
	return false
}

// ValidateSessionData は開催回データを検証する（docs/data-structure.md）。
func (Playbook) ValidateSessionData(_ context.Context, _ coord.SessionParams, raw json.RawMessage) (json.RawMessage, error) {
	var d SessionData
	if err := decodeStrict(raw, &d); err != nil {
		return nil, decodeError(err)
	}
	v := &coord.ValidationError{}

	d.BookTitle = strings.TrimSpace(d.BookTitle)
	if n := textLen(d.BookTitle); n < 1 || n > maxBookTitleLen {
		v.Add("book_title", "書名は1〜%d文字で入力してください", maxBookTitleLen)
	}
	if d.ISBN != nil && !ValidISBN13(*d.ISBN) {
		v.Add("isbn", "ISBNはハイフンなしの13桁で入力してください")
	}
	switch d.TocSource.Kind {
	case "web":
		if len(d.TocSource.URLs) == 0 {
			v.Add("toc_source.urls", "取得元のURLが必要です")
		}
		for i, u := range d.TocSource.URLs {
			if !isHTTPSURL(u) {
				v.Add(fmt.Sprintf("toc_source.urls[%d]", i), "https のURLを指定してください")
			}
		}
	case "image", "manual":
		if len(d.TocSource.URLs) != 0 {
			v.Add("toc_source.urls", "web 以外では空にしてください")
		}
	default:
		v.Add("toc_source.kind", "web・image・manual のいずれかを指定してください")
	}
	d.TocSource.URLs = nonNil(d.TocSource.URLs)

	if n := len(d.Sections); n < 1 || n > maxSections {
		v.Add("sections", "節は1〜%d件で登録してください", maxSections)
	}
	known := idSet{}
	for i := range d.Sections {
		s := &d.Sections[i]
		p := fmt.Sprintf("sections[%d]", i)
		if !validID(s.ID) {
			v.Add(p+".id", "IDは前後に空白のない1〜%d文字で指定してください", maxIDLen)
		} else if known.has(s.ID) {
			v.Add(p+".id", "節IDが重複しています")
		}
		known[s.ID] = struct{}{}
		s.Title = strings.TrimSpace(s.Title)
		if n := textLen(s.Title); n < 1 || n > maxSectionTitleLen {
			v.Add(p+".title", "節タイトルは1〜%d文字で入力してください", maxSectionTitleLen)
		}
	}
	d.CompletedSectionIDs = nonNil(d.CompletedSectionIDs)
	d.TargetSectionIDs = nonNil(d.TargetSectionIDs)
	checkIDList(v, "completed_section_ids", d.CompletedSectionIDs, known)
	checkIDList(v, "target_section_ids", d.TargetSectionIDs, known)
	if len(d.TargetSectionIDs) == 0 {
		v.Add("target_section_ids", "今回扱う範囲を1件以上選んでください")
	}
	completed := newIDSet(d.CompletedSectionIDs)
	for i, id := range d.TargetSectionIDs {
		if completed.has(id) {
			v.Add(fmt.Sprintf("target_section_ids[%d]", i), "前回までの範囲と重なっています")
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return mustJSON(d), nil
}

func isHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func sessionData(s coord.Snapshot) (SessionData, error) {
	var d SessionData
	if err := json.Unmarshal(s.SessionData, &d); err != nil {
		return d, fmt.Errorf("reading: 開催回データを読めません: %w", err)
	}
	return d, nil
}

func (d SessionData) sectionSet() idSet {
	s := idSet{}
	for _, sec := range d.Sections {
		s[sec.ID] = struct{}{}
	}
	return s
}

// ValidatePreparation は参加条件を検証する（docs/data-structure.md）。
func (Playbook) ValidatePreparation(_ context.Context, s coord.Snapshot, attendance string, raw json.RawMessage) (json.RawMessage, error) {
	sd, err := sessionData(s)
	if err != nil {
		return nil, err
	}
	var d PreparationData
	if err := decodeStrict(raw, &d); err != nil {
		return nil, decodeError(err)
	}
	d.PreparedSectionIDs = nonNil(d.PreparedSectionIDs)
	d.ExplainableSectionIDs = nonNil(d.ExplainableSectionIDs)

	v := &coord.ValidationError{}
	known := sd.sectionSet()
	checkIDList(v, "prepared_section_ids", d.PreparedSectionIDs, known)
	checkIDList(v, "explainable_section_ids", d.ExplainableSectionIDs, known)
	prepared := newIDSet(d.PreparedSectionIDs)
	for i, id := range d.ExplainableSectionIDs {
		if !prepared.has(id) {
			v.Add(fmt.Sprintf("explainable_section_ids[%d]", i), "説明できる節は読んできた節から選んでください")
		}
	}
	if attendance == coord.AttendanceAttending && d.WillingToPresent {
		if len(d.ExplainableSectionIDs) == 0 {
			v.Add("explainable_section_ids", "担当できる場合は説明できる節を1件以上選んでください")
		}
		if d.MaxPresentationMinutes < 1 || d.MaxPresentationMinutes > s.DurationMinutes {
			v.Add("max_presentation_minutes", "持ち時間以内で指定してください")
		}
	} else {
		if d.WillingToPresent {
			v.Add("willing_to_present", "欠席する場合は担当できません")
		}
		if len(d.ExplainableSectionIDs) != 0 {
			v.Add("explainable_section_ids", "担当しない場合は空にしてください")
		}
		if d.MaxPresentationMinutes != 0 {
			v.Add("max_presentation_minutes", "担当しない場合は0にしてください")
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return mustJSON(d), nil
}

// ApplyWithdrawal は辞退時の変換（docs/data-structure.md）。準備済みの節は維持する。
func (Playbook) ApplyWithdrawal(_ context.Context, _ coord.Snapshot, _ string, current json.RawMessage) (json.RawMessage, error) {
	d := PreparationData{PreparedSectionIDs: []string{}}
	if len(current) > 0 {
		var cur PreparationData
		if err := json.Unmarshal(current, &cur); err != nil {
			return nil, fmt.Errorf("reading: 参加条件を読めません: %w", err)
		}
		d.PreparedSectionIDs = nonNil(cur.PreparedSectionIDs)
	}
	d.ExplainableSectionIDs = []string{}
	return mustJSON(d), nil
}

func decodePlan(raw json.RawMessage) (PlanData, error) {
	var p PlanData
	if err := decodeStrict(raw, &p); err != nil {
		return p, decodeError(err)
	}
	return p, nil
}

// presenters は担当者を出現順に重複なく返す。
func (p PlanData) presenters() []string {
	var ids []string
	seen := idSet{}
	for _, item := range p.Agenda {
		if item.PresenterMemberID == nil {
			continue
		}
		id := *item.PresenterMemberID
		if !seen.has(id) {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

// Assignees は担当者（presenter_member_id が指定された人）を返す（docs/data-structure.md）。
func (Playbook) Assignees(raw json.RawMessage) ([]string, error) {
	var p PlanData
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("reading: 計画を読めません: %w", err)
	}
	return p.presenters(), nil
}

func preparationData(s coord.Snapshot, memberID string) (*coord.Preparation, PreparationData) {
	p := s.Preparation(memberID)
	var d PreparationData
	if p != nil {
		_ = json.Unmarshal(p.Data, &d)
	}
	return p, d
}

// ValidatePlan は計画を検証する（docs/data-structure.md）。
func (pb Playbook) ValidatePlan(_ context.Context, s coord.Snapshot, prop coord.Proposal) error {
	sd, err := sessionData(s)
	if err != nil {
		return err
	}
	p, err := decodePlan(prop.Data)
	if err != nil {
		return err
	}
	v := &coord.ValidationError{}

	known := sd.sectionSet()
	checkIDList(v, "covered_section_ids", p.CoveredSectionIDs, known)
	checkIDList(v, "deferred_section_ids", p.DeferredSectionIDs, known)
	covered := newIDSet(p.CoveredSectionIDs)
	for i, id := range p.DeferredSectionIDs {
		if covered.has(id) {
			v.Add(fmt.Sprintf("deferred_section_ids[%d]", i), "今回扱う範囲と重なっています")
		}
	}
	union := append(append([]string{}, p.CoveredSectionIDs...), p.DeferredSectionIDs...)
	if !sameSet(union, sd.TargetSectionIDs) {
		v.Add("covered_section_ids", "今回扱う範囲と持ち越す範囲の合計が、開催回の対象範囲と一致しません")
	}

	if n := len(p.Agenda); n < 1 || n > maxAgendaItems {
		v.Add("agenda", "進行項目は1〜%d件にしてください", maxAgendaItems)
	}
	allowed := newIDSet(append(append([]string{}, p.CoveredSectionIDs...), sd.CompletedSectionIDs...))
	inAgenda := idSet{}
	itemIDs := idSet{}
	total := 0
	minutesByPresenter := map[string]int{}
	for i, item := range p.Agenda {
		path := fmt.Sprintf("agenda[%d]", i)
		if !validID(item.ID) {
			v.Add(path+".id", "IDは前後に空白のない1〜%d文字で指定してください", maxIDLen)
		} else if itemIDs.has(item.ID) {
			v.Add(path+".id", "進行項目のIDが重複しています")
		}
		itemIDs[item.ID] = struct{}{}
		switch item.Activity {
		case ActivityPresentation, ActivityReview, ActivityDiscussion, ActivityJointReading:
		default:
			v.Add(path+".activity", "presentation・review・discussion・joint_reading のいずれかを指定してください")
		}
		if len(item.SectionIDs) == 0 {
			v.Add(path+".section_ids", "節を1件以上指定してください")
		}
		checkIDList(v, path+".section_ids", item.SectionIDs, nil)
		for j, id := range item.SectionIDs {
			if !allowed.has(id) {
				v.Add(fmt.Sprintf("%s.section_ids[%d]", path, j), "今回扱う範囲か前回までの範囲の節を指定してください")
			}
			inAgenda[id] = struct{}{}
		}
		if item.Minutes < 1 {
			v.Add(path+".minutes", "1分以上にしてください")
		}
		total += item.Minutes
		if item.Activity == ActivityPresentation && item.PresenterMemberID == nil {
			v.Add(path+".presenter_member_id", "発表には担当者が必要です")
		}
		if item.PresenterMemberID == nil {
			continue
		}
		pid := *item.PresenterMemberID
		prep, pd := preparationData(s, pid)
		switch {
		case prep == nil || prep.Attendance != coord.AttendanceAttending:
			v.Add(path+".presenter_member_id", "担当者は参加予定のメンバーから選んでください")
		case !pd.WillingToPresent:
			v.Add(path+".presenter_member_id", "この人は今回担当できないと回答しています")
		default:
			explainable := newIDSet(pd.ExplainableSectionIDs)
			for j, id := range item.SectionIDs {
				if !explainable.has(id) {
					v.Add(fmt.Sprintf("%s.section_ids[%d]", path, j), "担当者が説明できる節ではありません")
				}
			}
			minutesByPresenter[pid] += item.Minutes
			if minutesByPresenter[pid] > pd.MaxPresentationMinutes {
				v.Add(path+".minutes", "担当者が説明できる時間（%d分）を超えています", pd.MaxPresentationMinutes)
			}
		}
	}
	for i, id := range p.CoveredSectionIDs {
		if !inAgenda.has(id) {
			v.Add(fmt.Sprintf("covered_section_ids[%d]", i), "今回扱う節が進行表に含まれていません")
		}
	}
	if total > s.DurationMinutes {
		v.Add("agenda", "合計%d分が持ち時間（%d分）を超えています", total, s.DurationMinutes)
	}
	if prop.ChangeKind == coord.ChangeReplan {
		for _, id := range substitutes(s, p) {
			if consecutiveSubstituteCount(s, id) >= maxConsecutiveSubstitutes {
				v.Add("agenda", "同じ人（%s）を3回連続で代役にできません", id)
			}
		}
	}
	return v.Err()
}

// substitutes は今回の案で新たに担当になる人（当初の確定計画に担当がなかった人）を返す。
func substitutes(s coord.Snapshot, p PlanData) []string {
	if len(s.ConfirmedPlans) == 0 {
		return nil
	}
	var initial PlanData
	_ = json.Unmarshal(s.ConfirmedPlans[0].Data, &initial)
	original := newIDSet(initial.presenters())
	var subs []string
	for _, id := range p.presenters() {
		if !original.has(id) {
			subs = append(subs, id)
		}
	}
	return subs
}

// sessionSubstitutes は過去の開催回で代役になった人。最終の確定計画の担当者のうち、当初の確定計画にいなかった人。
func sessionSubstitutes(ps coord.PastSession) idSet {
	if len(ps.ConfirmedPlans) < 2 {
		return idSet{}
	}
	var first, last PlanData
	_ = json.Unmarshal(ps.ConfirmedPlans[0].Data, &first)
	_ = json.Unmarshal(ps.ConfirmedPlans[len(ps.ConfirmedPlans)-1].Data, &last)
	original := newIDSet(first.presenters())
	subs := idSet{}
	for _, id := range last.presenters() {
		if !original.has(id) {
			subs[id] = struct{}{}
		}
	}
	return subs
}

// consecutiveSubstituteCount は直近の確定済み開催回から数えて、続けて代役になった回数。
// History は新しい順に並ぶ。
func consecutiveSubstituteCount(s coord.Snapshot, memberID string) int {
	n := 0
	for _, ps := range s.History {
		if !sessionSubstitutes(ps).has(memberID) {
			break
		}
		n++
	}
	return n
}

// ApprovalRequirements は案に必要な条件を返す（docs/data-structure.md）。
func (Playbook) ApprovalRequirements(_ context.Context, s coord.Snapshot, prop coord.Proposal) (coord.ApprovalRequirements, error) {
	p, err := decodePlan(prop.Data)
	if err != nil {
		return coord.ApprovalRequirements{}, err
	}
	req := coord.ApprovalRequirements{RequiredAcceptorIDs: nonNil(p.presenters()), Approvals: []coord.ApprovalRequirement{}}
	switch prop.ChangeKind {
	case coord.ChangeInitial:
		req.Approvals = append(req.Approvals, coord.ApprovalRequirement{Kind: coord.ApprovalOwner})
	case coord.ChangeReplan:
		cur := s.CurrentPlan()
		if cur == nil {
			return coord.ApprovalRequirements{}, fmt.Errorf("reading: 再計画の比較対象となる確定計画がありません")
		}
		var prev PlanData
		if err := json.Unmarshal(cur.Data, &prev); err != nil {
			return coord.ApprovalRequirements{}, fmt.Errorf("reading: 確定計画を読めません: %w", err)
		}
		if !onlyPresentersChanged(prev, p) {
			req.Approvals = append(req.Approvals, coord.ApprovalRequirement{
				Kind:              coord.ApprovalMajority,
				EligibleMemberIDs: nonNil(s.Attending()),
			})
		}
	default:
		return coord.ApprovalRequirements{}, fmt.Errorf("reading: 不明な案の種類 %q", prop.ChangeKind)
	}
	return req, nil
}

// onlyPresentersChanged は範囲・進行表の内容と時間配分が同じで、担当者だけが違うかを返す。
func onlyPresentersChanged(a, b PlanData) bool {
	if !sameSet(a.CoveredSectionIDs, b.CoveredSectionIDs) || !sameSet(a.DeferredSectionIDs, b.DeferredSectionIDs) {
		return false
	}
	if len(a.Agenda) != len(b.Agenda) {
		return false
	}
	for i := range a.Agenda {
		x, y := a.Agenda[i], b.Agenda[i]
		if x.Activity != y.Activity || x.Minutes != y.Minutes || !sameSet(x.SectionIDs, y.SectionIDs) || len(x.SectionIDs) != len(y.SectionIDs) {
			return false
		}
	}
	return true
}
