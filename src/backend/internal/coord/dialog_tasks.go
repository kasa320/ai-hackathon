package coord

import (
	"context"
	"fmt"

	"github.com/kasa320/ai-hackathon/src/backend/internal/apitypes"
)

// DialogTask is a server-bound proposal/task pair, never extracted from a message.
type DialogTask struct {
	Task  apitypes.Task
	Lines []string
}

func (c *Coordinator) DialogTasks(ctx context.Context, userID, sessionID string) ([]DialogTask, error) {
	d, err := c.SessionDetail(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	p := d.CurrentProposal
	if p == nil || p.Status != "pending" {
		return nil, nil
	}
	pb, err := c.playbook(d.Session.PlaybookID)
	if err != nil {
		return nil, err
	}
	describer, ok := pb.(PlanDescriber)
	if !ok {
		return nil, nil
	}
	names := map[string]string{}
	for _, m := range d.Members {
		names[m.ID] = m.DisplayName
	}
	lines, err := describer.DescribePlan(d.Data, p.Data, d.Session.DurationMinutes, names)
	if err != nil {
		return nil, err
	}
	if d.Session.ScheduleStatus != ScheduleProposed {
		lines = append([]string{"開催日時：" + formatClock(d.Session.StartsAt)}, lines...)
	}
	var cards []DialogTask
	for _, t := range d.MyTasks {
		if t.Kind == "preparation" || t.Status != "open" || !d.ServerNow.Before(t.DueAt) || t.ProposalID == nil || t.ProposalVersion == nil || *t.ProposalID != p.ID || *t.ProposalVersion != p.Version {
			continue
		}
		card := DialogTask{Task: t, Lines: append([]string{fmt.Sprintf("【%s・案%d】", d.Session.Title, p.Version), t.Title}, lines...)}
		card.Lines = append(card.Lines, "回答期限："+formatClock(t.DueAt))
		switch t.Kind {
		case "assignment":
			card.Lines = append(card.Lines, "回答してほしいこと：上の進行で、あなたに割り当てられた範囲・分数の説明を担当できますか？下の「担当を引き受ける」か「担当を辞退する」を押してください。")
		case "owner_approval":
			card.Lines = append(card.Lines, "回答してほしいこと：管理者として、上の日程・進行・担当の案を承認できますか？下の承認／不承認ボタンを押してください。参加可否や担当の引き受けは、それぞれの確認にも回答してください。")
		case "approval":
			question := "上の進行・担当の案に同意できますか？"
			for _, a := range p.Approvals {
				if a.Kind == "all" {
					question = "上の日時に、記載された時間・内容で参加できますか？"
				}
			}
			card.Lines = append(card.Lines, "回答してほしいこと："+question+" 下の同意ボタンか「同意できない」を押してください。条件がある・まだ分からない場合は同意せず、「参加条件」から条件を更新してください。")
		}
		cards = append(cards, card)
	}
	return cards, nil
}
