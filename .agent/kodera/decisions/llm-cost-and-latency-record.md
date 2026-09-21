# LLM 呼び出しの確定費用と所要時間を記録する

更新日: 2026-09-21
対象ブランチ: `feat/member-leave`

## 背景と解決したい問題

`llm_calls` の費用が全件「不明」のままだった。原因はプロンプトや保存処理ではなく、**OrcaRouter の `chat/completions` 応答に金額が含まれていない**こと。

実測した `usage` は次のとおりで、トークン数はあるが `cost` が無い。

```json
"usage": {"prompt_tokens": 13, "completion_tokens": 5, "total_tokens": 18,
          "prompt_tokens_details": {"cached_tokens": 0},
          "completion_tokens_details": {"reasoning_tokens": 4}}
```

`agent.Client.Chat` は `usage.cost` を読む実装だったため、`estimated_amount` は常に NULL、`currency` は常に `unknown` になっていた。

この状態では [evaluation.md 第6節](../../decisions/evaluation.md) の「取得可能な請求額を記録する」「見積額と確定請求額を区別する」を満たせず、費用の節が「未計測」のまま提出になる。OrcaRouter 資料 p11 の評価観点「『なんとなく安い』ではなく根拠を示せるか」「『削った』と言うより『測って出せる』方が強い」に対して、出せる数字が何も無い。

また所要時間も記録していなかった。資料 p12 の「`auto` の『最安』は『最速』ではない」を自分のデータで確認するには所要時間が要る。

## 決定事項

- `GET /v1/generation?id={request_id}` から確定費用と所要時間を取得し、記録に反映する。
- 取得先の対応は次のとおり。

| 応答の項目 | 書き込み先 |
| --- | --- |
| `total_cost` | `llm_calls.billed_amount`（確定請求額） |
| `cost_currency` | `llm_calls.currency`（`unknown` を置き換える） |
| `latency_ms` | `llm_calls.latency_ms`（新設） |
| `model` | `resolved_model` が空のときだけ補完 |

- `latency_ms` 列を新設する。NULL 可とし、取得できなければ NULL のままにする。
- 取得は **LLM 呼び出しの直後**に行う（後述の理由により、定期的な回収処理は作らない）。
- **取得に失敗しても本処理は止めない**。エラーを呼び出し元へ返さず、記録を変えずに戻る。費用が分からないことは計画の可否とは無関係であり、ここで失敗させると LLM 呼び出し自体が無駄になる。
- 費用不明を 0 や推定値で埋めない。`billed_amount` は取れたときだけ入れる。
- `estimated_amount`（見積）の扱いは変えない。`usage.cost` が返るようになれば従来どおり入る。確定額とは別の列のまま維持する。
- タイムアウトは10秒。親コンテキストのキャンセルを引き継ぐ。

## 実装方針

- `agent.Client.fetchGeneration` を追加し、`Chat` の成功時に呼ぶ。`request_id` が空なら何もしない。
- 200 以外、本文が読めない、JSON が壊れている、のいずれでも黙って戻る。
- `total_cost` は float で返るため、`strconv.FormatFloat(v, 'f', -1, 64)` で文字列にして保存する。既存の `estimated_amount` と同じ扱いにし、浮動小数の丸めを保存時に持ち込まない。
- `latency_ms` を `coord.LLMCall` と `store.LLMCall` に足し、計画・解釈・目次の3経路すべてで保存されるよう配線する。
- 既存 DB への列追加は `addMissingColumns` に1行足して対応する（[llm-call-routing-record.md](llm-call-routing-record.md) と同じ方針）。

### 「後からまとめて回収する」案を採らなかった理由

ドキュメントには「決済済みコストを読める」とあり、呼び出し直後では未確定の可能性を懸念した。しかし実測では**直後の取得で `total_cost` が返った**（下記の検証記録）。

提出まで1日という状況で、NULL の行を定期的に拾い直す回収処理を新たに作るより、直後に1回取りに行って失敗を許容する方が、実装量・確認量ともに小さい。取れなかった行は `billed_amount` が NULL のまま残り、集計時に「不明」として扱える。

## 実装対象ファイル

- `src/backend/internal/agent/llm.go`
- `src/backend/internal/store/schema.sql`
- `src/backend/internal/store/store.go`
- `src/backend/internal/store/events.go`
- `src/backend/internal/coord/planner.go`
- `src/backend/internal/coord/runner.go`
- `src/backend/internal/coord/interpret.go`
- `src/backend/internal/playbook/reading/toc/service.go`

## 検証記録

2026-09-21:

- `go build ./...`、`go vet ./...` 成功。
- `go test ./... -count=1` 全パッケージ成功。
- **API の実測**：`orcarouter/auto` へ最小のリクエストを送り、`usage` に `cost` が無いことを確認。続けて `GET /v1/generation?id=...` を叩き、`total_cost`・`cost_currency`・`latency_ms`・`model` が返ることを確認した。
- **エンドツーエンドの確認**：`data/app.db` の複製に対し `AGENT_MODE=llm` でサーバーを起動し、`replan_demo` 投入後に B の担当辞退を登録して実際に再計画を走らせた。

```
purpose=planner  要求=orcarouter/auto
  解決=z-ai/glm-5.3-flash  fallback=0  所要=73000ms  in=2506 out=3215
  確定額=0.000992 USD  ok=1
```

  費用・通貨・所要時間がすべて埋まった。本物の `data/app.db` には触れておらず、検証用の複製・ログ・Cookie は確認後に削除し、テスト用サーバーも停止した。

## 実測で分かったこと（記事に使える）

### `orcarouter/auto` は73秒かかった

同じ再計画1件で、`auto` が選んだ `z-ai/glm-5.3-flash` は **73秒** かかった。資料 p12 の実測例（`auto` が45.96秒 → `gemini-2.5-flash-lite` 固定で2.48秒）と同じ傾向が、このアプリでも再現している。

**これは README に書いた「Discord の1往復は60秒で打ち切る」を超えている。** 解釈処理を `auto` のまま Discord の DM で使うと、打ち切りに間に合わない可能性が高い。Named Router で応答の速いモデルを Mundane に置く判断の、直接の根拠になる。

### 費用は1件あたり $0.001 未満

再計画1回で $0.000992（約0.15円）。プロモクレジット $20 に対して、実測27本を回しても費用面の制約にはならない。**計画側は品質優先でモデルを選んでよい**という判断を数字で裏づけられる。

## 評価項目でのアピールポイント

### ② コストパフォーマンス（主）

- 全呼び出しについて**確定請求額**を保存する。価格表からの計算（「推定」）ではなく、OrcaRouter が決済した金額そのものを記録するため、evaluation.md が求める「見積額と確定請求額の区別」を満たせる。
- `latency_ms` により、費用と速度を同じ表で比較できる。「安いが遅い」構成を数字で棄却できる。
- 73秒という実測値は、`auto` 任せの構成が Discord の応答制限を超えるという具体的な不都合を示している。ルーター設計が**好みではなく必要**だったことの根拠になる。

### ③ 信頼性・堅牢性（副）

- 費用の取得失敗を本処理から切り離した。外部APIの付随的な失敗が、業務フロー（計画・同意・通知）を止めない構造になっている。
- 取得できなかった費用を 0 で埋めず NULL のまま残すため、「測れなかった」と「0円だった」を混同しない。

## 却下した案

### 案1: `billed_amount` が NULL の行を定期的に拾って回収する

決済確定を確実に待てる利点があるが、新しい定期処理と再試行の設計が必要になる。実測では直後の取得で確定額が返ったため、提出までの時間に対して割に合わない。ただし取りこぼしが多いと分かった場合は、この案に切り替える。

### 案2: 価格表とトークン数から費用を計算して `estimated_amount` に入れる

確定額が取れなくても数字は出せるが、evaluation.md は「価格表から計算した値なら『推定』と記す」と定めている。確定額が実際に取得できる以上、推定で代用する理由がない。

### 案3: 取得失敗時に LLM 呼び出し自体を失敗として扱う

費用が不明な記録を残さずに済むが、計画としては成功しているものを捨てることになる。呼び出しの費用は既に発生しており、破棄すると同じ費用をもう一度払う。

### 案4: `ttft_ms` も保存する

応答が返り始めるまでの時間も取得できるが、このアプリはストリーミングを使っておらず、利用者が体感するのは `latency_ms` の方。列を増やす利益が小さいため今回は入れない。

## 後で必ず検討・実装すること

- `extra_body.models` と `extra_body.route="fallback"` による別ベンダーへのフェイルオーバー。`fallback_level` を記録する器はあるが、発動させる仕組みがまだ無い。
- Named Router（`marunage-plan` / `marunage-extract`）の作成と `.env` への設定（OrcaRouter のコンソール作業）。
- 計測結果を [orcarouter-model-cost-measurements.md](orcarouter-model-cost-measurements.md) の表へ記入する。所要時間の列を `latency_ms` に合わせて追加するか要検討。
- 費用と選択モデルを管理者画面に出すかどうかは未決。現在は記録のみで、API・画面には出していない。
- 取得失敗が続く場合の扱い（回収処理の追加、または失敗率のログ出力）。現時点では失敗を数えていない。

## push までの運用

- コミットと push はユーザーと確認してから行う。
