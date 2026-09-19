// Package agent は LLM によるエージェント。調整案件の状況から次に使うツールを選ぶ。実行してよいかの判定は coord が行う。
// 用途固有の文脈と指示は coord.Playbook から取得し、reading を直接 import しない。
// LLM呼び出し・ツール実行ループは未実装。
package agent
