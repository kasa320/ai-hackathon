package reading

import (
	"encoding/json"
	"fmt"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"time"
)

func (Playbook) PreparationRequest(s coord.Snapshot, memberID string) string {
	p, d := preparationData(s, memberID)
	if s.ScheduleStatus == coord.ScheduleProposed && (p == nil || d.Schedule == nil || d.Schedule.Status == "unknown") {
		text := fmt.Sprintf("%s〜%sの開催日時を決めるため、次の予定を教えてください。\n・参加できる曜日または日付と時間帯（日本時間）\n・1回に参加できる最大時間\n・終日参加できない日（なければ「なし」）\n時間帯の回答例：水 20:00-22:00 60分\nまだ分からなければ「未定」と答えられます。", s.PeriodStart, s.PeriodEnd)
		if p == nil {
			text += "\n続けて、輪読への参加希望・読んだ範囲・説明を担当できる範囲と分数も確認します。"
		}
		return text
	}
	return "今回の進行と担当を決めるため、次の内容を教えてください。\n・参加できるか（参加／欠席）\n・読んできた範囲\n・説明を担当できるか（はい／いいえ）\n・担当できる場合、その範囲と説明できる分数\n担当できない場合も、そのまま回答してください。欠席や辞退の理由は不要です。"
}

func (Playbook) DescribePlan(sessionRaw, planRaw json.RawMessage, duration int, memberNames map[string]string) ([]string, error) {
	var s SessionData
	if err := json.Unmarshal(sessionRaw, &s); err != nil {
		return nil, err
	}
	p, err := decodePlan(planRaw)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	for _, sec := range s.Sections {
		titles[sec.ID] = sec.Title
	}
	lines := []string{fmt.Sprintf("開催時間：%d分", duration)}
	if p.StartsAt != "" {
		at, err := time.Parse(time.RFC3339, p.StartsAt)
		if err != nil {
			return nil, err
		}
		lines = append(lines, "開催日時："+at.In(jst).Format("2006/01/02 15:04")+"（JST）")
	}
	names := map[string]string{ActivityPresentation: "発表", ActivityReview: "復習", ActivityDiscussion: "議論", ActivityJointReading: "共同読み"}
	for _, item := range p.Agenda {
		presenter := ""
		if item.PresenterMemberID != nil {
			presenter = "・担当：" + memberNames[*item.PresenterMemberID]
		}
		lines = append(lines, fmt.Sprintf("%s：%s（%d分%s）", names[item.Activity], sectionsLabel(titles, item.SectionIDs), item.Minutes, presenter))
	}
	lines = append(lines, "持ち越す範囲："+sectionsLabel(titles, p.DeferredSectionIDs))
	return lines, nil
}
