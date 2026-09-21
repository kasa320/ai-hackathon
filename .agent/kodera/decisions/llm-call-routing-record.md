# LLM 呼び出しに用途と実際の応答モデルを記録する

更新日: 2026-09-21
対象ブランチ: `feat/member-leave`

## 背景と解決したい問題

OrcaRouter の Named Router を導入する方針を決めたが、現状の `llm_calls` では計測ができない。理由は2つある。

### 1. 実際に応答したモデルが残らない

`llm_calls.model` には「要求したモデル」しか入らない。`orcarouter/auto` や Named Router を指定した場合、そこに入るのはルーター名であって、実際に応答したモデルではない。

このままでは「安い担当が何割、強い担当が何割」という集計ができず、[evaluation.md 第6節](../../decisions/evaluation.md) が求めるモデル別の費用比較も、OrcaRouter 資料 p11 の「削ったと言うより測って出せる」も成立しない。

### 2. 4つの呼び出し元のうち2組が区別できない

LLM を呼ぶのは計画・解釈・目次検索・目次画像の4か所だが、既存の `case_id` / `lookup_id` では次のように潰れていた。

| 呼び出し元 | 従来の判別 |
| --- | --- |
| 計画（planner） | `case_id` が入る |
| 解釈（interpreter） | `case_id` が入る（計画と区別できない） |
| 目次検索（toc_search） | `lookup_id` が入る |
| 目次画像（toc_vision） | `lookup_id` が入る（検索と区別できない） |

計測表を計画用と解釈用に分けて用意したのに、DB 側でその2つを分離できない状態だった。

## 決定事項

- `llm_calls` に4列を追加する。`purpose` / `resolved_model` / `fallback_level` / `request_id`。
- 4列はすべて NULL 可とする。既存の行は NULL のまま残し、0 や空文字として扱わない。
- `purpose` は呼び出し元が付ける。値は `planner` / `interpreter` / `toc_search` / `toc_vision` の4つで、定数を `internal/coord` に置く。
- `resolved_model` はヘッダーから取れたときだけ入れる。**取れないときに `model` で代用しない**。代用すると「ルーター名を実測値として集計してしまう」ため。
- 受け皿が発動した場合は、実際に応答したモデル（`X-Orca-Fallback-Model`）を `resolved_model` に入れる。
- 障害注入（`fault-injection`）の記録にも `purpose` を付け、計測時に除外できるようにする。
- 画面と API には出さない。今回は記録だけに絞る。

### テーブルを分けず、列で分ける

目次取得用の記録を別テーブルへ分ける案を検討したが採用しない。理由は「実装方針」ではなく「却下した案」に記す。

## 実装方針

### DB

- `schema.sql` の `llm_calls` に4列を追加する（新規 DB 用）。
- 既存 DB には `store.go` の `addMissingColumns` へ4行足して対応する。このリポジトリには既に同じ仕組みがあり（`sessions.period_start` ほか）、今回追加する4列はすべて NULL 可の列追加なので、この仕組みの制約内に収まる。
- `pressly/goose/v3` は導入しない。この仕組みで扱えるのは「列の追加」だけであり、列の削除・改名・型変更・データ移行・ロールバックはできない。実データ投入前に goose を入れるという既存方針は維持する。

### ヘッダーの読み取り

`agent.Client.Chat` に `readRoutingHeaders` を足し、レスポンスヘッダーから記録へ移す。

- `X-Orca-Request-Id` → `request_id`
- `X-Orca-Resolved-Model` → `resolved_model`
- `X-Orca-Fallback-Model` → `resolved_model`（あれば上書き）
- `X-Orca-Fallback-Level` → `fallback_level`

**実測の結果、`X-Orca-Fallback-Level` は返らなかった。** 代わりに `X-Orca-Route` が次の形式で返る。

```
X-Orca-Route: model=z-ai/glm-5.3-flash; by=balanced; class=chat; fallback=0
```

そのため、専用ヘッダーが無い場合に限り `X-Orca-Route` の `fallback=` と `model=` から補う実装にした。ドキュメント（`docs.orcarouter.ai/routing/response-headers.md`）の記載と実際の応答が一致しなかったため、実測に合わせている。

## 実装対象ファイル

- `src/backend/internal/store/schema.sql`
- `src/backend/internal/store/store.go`
- `src/backend/internal/store/events.go`
- `src/backend/internal/coord/planner.go`
- `src/backend/internal/coord/runner.go`
- `src/backend/internal/coord/interpret.go`
- `src/backend/internal/agent/llm.go`
- `src/backend/internal/agent/planner.go`
- `src/backend/internal/agent/interpreter.go`
- `src/backend/internal/playbook/reading/toc/llm.go`
- `src/backend/internal/playbook/reading/toc/service.go`

## 検証記録

2026-09-21:

- `go build ./...`、`go vet ./...` 成功。
- `go test ./... -count=1` 全パッケージ成功（agent、api、auth、clock、config、coord、devapi、discord、notify、reading、toc、store、E2E）。Application Control による実行拒否は発生しなかった。
- **ヘッダーの実測**：`orcarouter/auto` へ最小のリクエストを送り、返却されたヘッダーを確認した。`X-Orca-Request-Id`、`X-Orca-Resolved-Model`、`X-Orca-Route`、`X-Orca-Router`、`X-Orca-Version` が返り、`X-Orca-Fallback-Level` は返らなかった。
- **移行の確認**：既存の `data/app.db` を WAL ごと一時ディレクトリへ複製し（11列であることを確認）、新しいコードで開いて4列が追加されること、既存行が失われないことを確認した。本物の `data/app.db` には触れていない。
- **エンドツーエンドの確認**：複製した DB に対し `AGENT_MODE=llm` でサーバーを起動し、`replan_demo` を投入したうえで B の担当辞退を登録して、実際に計画処理を走らせた。記録された内容は次のとおり。

```
purpose=planner  要求=orcarouter/auto
  解決=z-ai/glm-5.3-flash   fallback=0
  req=20260921113026632111  in=2516 out=3859 ok=1  est=None cur=unknown
```

  案件は `awaiting_consent` まで進んだ。`orcarouter/auto` が選んだのは `z-ai/glm-5.3-flash` で、この情報は従来どこにも残っていなかった。
- 検証に使った一時 DB・ログ・Cookie は確認後に削除し、テスト用サーバーも停止した。

## 評価項目でのアピールポイント

### ② コストパフォーマンス（主）

- 「どのモデルが実際に応答したか」を全呼び出しについて保存するため、**モデル別の費用と到達率を実データで出せる**。資料 p17 の評価軸「『入れておくだけ』より、実測して設計に反映しているか」に直接対応する。
- `purpose` により、計画・解釈・目次検索・目次画像を分けて集計できる。「解釈は安いモデルで足り、計画だけ強いモデルが要る」という設計判断を、推測ではなく数値で示せる。
- 実測の初回で `orcarouter/auto` が `z-ai/glm-5.3-flash` を選んでいたことが判明した。**`auto` 任せでは「何を使っているか把握していない」状態だった**という、ルーターを自分で設計する動機そのものが記録として残っている。

### ③ 信頼性・堅牢性（副）

- `fallback_level` により、受け皿が実際に発動した回数を記録できる。フェイルオーバーを「設定した」ではなく「発動し、処理が継続した」実績として示せる。
- `request_id` を保持するため、障害発生時に OrcaRouter 側のログと突き合わせられる。

## 却下した案

### 案1: `model` 列に実際のモデルを上書きする（列を増やさない）

要求したモデル（ルーター名）が失われる。「どのルーターに投げた結果この振り分けになったか」が追えなくなり、ルーター設計の比較ができない。

### 案2: `model` に「ルーター名→解決モデル」の形で1列にまとめる

列は増えないが、集計のたびに文字列を分解する必要がある。計測データを表計算とグラフにする方針と噛み合わない。

### 案3: 目次取得用の呼び出しを別テーブルに分ける

検討したが採用しない。

- 案件あたりの合計費用を出すのに UNION が必要になる。記事では「この調整1件にいくらかかったか」を出したいので不利になる。
- 呼び出し回数の上限判定（`MaxLLMCallsPerCase`）が現在 `llm_calls` 1つを数えるだけで済んでいる。分けると2か所を数える実装になり、上限の抜け漏れが起きやすくなる。
- 分離の目的は集計であり、`WHERE purpose = 'toc_vision'` で足りる。既存行の移動を伴う分割は、列追加より明確に重い。

### 案4: `resolved_model` が取れないとき `model` で埋める

集計時に NULL を扱わずに済むが、ルーター名を実測値として数えてしまう。「不明を 0 や推定値で埋めない」という evaluation.md の原則に反するため採用しない。

## 後で必ず検討・実装すること

- `request_id` を使った確定請求額の取得。`GET /v1/generation` に渡すと決済済みコストが読める。現在は応答の `usage.cost` が返らず `estimated_amount` も NULL のままで、`billed_amount` は全件 NULL。evaluation.md が求める「見積額と確定請求額の区別」は未達のまま。
- `extra_body.models` と `extra_body.route="fallback"` による別ベンダーへのフェイルオーバー。`fallback_level` を記録する器はできたが、発動させる仕組みはまだ無い。
- Named Router（`marunage-plan` / `marunage-extract`）の作成と `.env` への設定。これは OrcaRouter のコンソール作業。
- 計測結果を [orcarouter-model-cost-measurements.md](orcarouter-model-cost-measurements.md) の表へ記入する。現在は全て未計測。
- 画面・API への露出は未対応。管理者が費用と選択モデルを画面で確認できるようにするかは未決。

## push までの運用

- コミットと push はユーザーと確認してから行う。
