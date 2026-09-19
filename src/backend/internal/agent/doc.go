// Package agent は LLM によるエージェント。調整案件の状況から次に使うツールを選ぶ。実行してよいかの判定は coord が行う。
// 用途固有の文脈と指示は coord.Playbook から取得し、reading を直接 import しない。
// LLM には同意・引き受けを記録するツールを与えず、「提案」「確認依頼」「管理者へ戻す」の3つだけを選ばせる。
package agent
