package reading

import (
	"encoding/json"
	"fmt"
	"github.com/kasa320/ai-hackathon/src/backend/internal/coord"
	"time"
)

// PreparationRequest は参加条件の確認依頼の文面。1行目は目的（Web ではこの行だけを出す）。
func (Playbook) PreparationRequest(s coord.Snapshot, memberID string) string {
	p, d := preparationData(s, memberID)
	if s.ScheduleStatus == coord.ScheduleProposed && (p == nil || d.Schedule == nil || d.Schedule.Status == "unknown") {
		return fmt.Sprintf("%s〜%sのどこかで開く日程を決めるため、参加できそうな日や時間帯を教えてください。\n「平日の夜なら」「土曜の午後」のように、ふだんの言葉で返信してもらえれば大丈夫です。\nまだ分からなければ「未定」、今回は難しければ「欠席」と送ってください。", shortDate(s.PeriodStart), shortDate(s.PeriodEnd))
	}
	return "今回の会に参加できるか教えてください。\n「参加します」「欠席します」のように返信してもらえれば大丈夫です。欠席の理由は不要です。"
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
