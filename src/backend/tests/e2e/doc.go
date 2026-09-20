// Package e2e はバックエンド全体を HTTP 経由で動かす結合テスト。
//
// 実際のサーバーと同じ部品（API・認証・調整処理・通知・目次の取得・開発用API）を組み立て、
// 外部サービス（Discord・LLM・書誌DB・取得元ページ）だけを偽物に差し替える。
// 時計は固定し、イベント処理・通知送信は各テストが明示的に進める。
//
// ファイルの分け方：
//   - harness_test.go       テスト用サーバーとHTTPクライアント
//   - auth_groups_test.go   ログイン・招待・グループ・開催回の登録
//   - replan_flow_test.go   中心の流れ（初回確定 → 辞退 → 確認 → 再計画 → 同意 → 確定 → 通知）E01・E02
//   - stop_cases_test.go    管理者判断待ち・期限・AI 障害（E03・E04・E10）
//   - safety_test.go        権限・本人確認・CSRF・同意の代行防止（E07・E08）
//   - consistency_test.go   再送・二重送信・旧版への回答（E05・E06）
//   - notify_restart_test.go 通知の失敗・成否不明と再起動からの再開（E11・E12）
//   - toc_test.go           輪読の目次の取得（R7）
//   - contract_test.go      応答の形（docs/data-structure.md の型・エラー形式）
//
// 実行：make test-e2e（または go -C src/backend test ./tests/e2e/ -v）
package e2e
