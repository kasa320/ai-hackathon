# LLM を呼ぶ4か所の宛先を起動時に記録する

更新日: 2026-09-21
対象ブランチ: `feat/member-leave`

## 背景と解決したい問題

このアプリが LLM を呼ぶ箇所は4つある。計画（`LLMPlanner`）、自由文の解釈（`LLMInterpreter`）、目次の Web 検索（`LLMSearcher`）、目次画像の書き写し（`LLMImageReader`）。それぞれ別のモデルを指定できる。

この4か所へ別々のモデルを割り当てる仕組み自体は、`origin/master` からのマージ（`07751ff`）で既に入っていた。環境変数は `ORCAROUTER_PLANNER_MODEL`、`ORCAROUTER_INTERPRETER_MODEL`、`ORCAROUTER_SEARCH_MODEL`、`ORCAROUTER_VISION_MODEL` の4つで、未設定なら `ORCAROUTER_MODEL` を引き継ぐ。

問題は、起動ログに計画と解釈の2つしか出ていなかったこと。特に目次画像には次の遅延故障がある。

- `ORCAROUTER_VISION_MODEL` が未設定だと `ORCAROUTER_MODEL` を引き継ぐ
- `ORCAROUTER_MODEL` に Named Router を設定すると、目次画像の書き写しもそのルーターに委ねられる
- ルーターの候補モデルに画像入力非対応のものが含まれていると失敗するが、**起動時には何も起きない**
- 実際に利用者が目次ページの写真を提出した瞬間に初めて失敗する

OrcaRouter の Named Router を導入する方針を決めたため、この構成ミスが現実的に起こりうる状態になった。

## 決定事項

- LLM を呼ぶ4か所すべての「実際の宛先モデル」を、起動時に1行の INFO ログへ出す。
- 目次検索が未設定の場合は `(なし)` と表示し、空文字で紛れないようにする。
- `ORCAROUTER_VISION_MODEL` が未設定で、かつ引き継いだ値が `orcarouter/` で始まる（＝ルーター指定）場合は、WARN を1行出す。
- 設定ミスを起動失敗にはしない。ルーターの候補に画像対応モデルしか入れていない構成は正当であり、起動を止めると運用の自由度を奪うため。
- ログに出すのはモデル名だけとする。APIキーやプロンプトは出さない。

## 実装方針

- ログの出力位置を、`vision` の解決が終わった後へ移す。解決前に出すと、引き継ぎ後の実際の値ではなく設定値がそのまま出てしまう。
- `strings.HasPrefix(vision, "orcarouter/")` でルーター指定かどうかを判定する。`orcarouter/auto`、`orcarouter/free`、`orcarouter/fusion*`、および自作の Named Router がすべてこの接頭辞を持つ。
- 判定の条件に `cfg.OrcaRouterVisionModel == ""` を含める。利用者が意図して vision にルーターを指定した場合は警告しない。

## 実装対象ファイル

- `src/backend/cmd/server/main.go`（1か所のみ）

他のファイルは変更していない。設定の読み込み（`internal/config/config.go`）、`.env.example`、README の環境変数表は、マージで取り込んだ時点で4か所分がすでに記載済みだった。

## 検証記録

2026-09-21:

- `go build ./...` 成功。
- `go vet ./cmd/server/` 成功。
- `go test ./... -count=1` 全パッケージ成功。今回は Windows Application Control による実行拒否は発生しなかった。
- 一時DBを使って `AGENT_MODE=llm` で実際に起動し、次の出力を確認した（`ORCAROUTER_PLANNER_MODEL` のみ設定し、他は未設定の構成）。

```
level=INFO msg="LLM の設定" planner_model=orcarouter/marunage-plan interpreter_model=orcarouter/auto
  vision_model=orcarouter/auto search_model=(なし) timeout=3m0s
level=WARN msg="ORCAROUTER_VISION_MODEL が未設定のため、目次画像の書き写しがルーターに委ねられます。
  画像入力に対応するモデルを明示してください" vision_model=orcarouter/auto
```

- 4か所すべてが表示されること、未設定の目次検索が `(なし)` になること、vision の警告が意図通り発火することを確認した。
- 既存の `data/app.db` には触れていない。検証用DBは一時ディレクトリに作成し、確認後に削除した。

## 評価項目でのアピールポイント

### ③ 信頼性・堅牢性（主）

この変更は「**遅延故障を、起きる前に表面化させる**」層を足したものとして説明できる。

- 目次画像の構成ミスは、これまで**利用者が写真を提出した瞬間**まで検知できない遅延故障だった。失敗するのは運用中で、しかも原因が設定にあると気づきにくい。
- 起動時点で宛先を全件表示し、危険な構成には警告を出すことで、**故障が発生する前に構成の誤りを検知**できるようになった。
- 審査観点「想定外の入力やエラーでも安定して処理・復旧できるか」に対し、実行時の復旧（再試行・フェイルオーバー・管理者への安全な差し戻し）だけでなく、**構成ミス由来の故障を運用前に潰す層**を持っていることを示せる。
- 起動を失敗させず警告にとどめた判断も、堅牢性の設計として説明できる。正当な構成を拒否して起動不能にする方が、運用上の事故は大きい。

### ② コストパフォーマンス（副）

- 費用・所要時間の計測結果について「**どの構成で測った数値か**」の証跡が実行ログに残る。
- OrcaRouter 資料 p17 の評価軸「『入れておくだけ』より、実測して設計に反映しているか」に対し、構成を記録したうえで比較したことを示せる。
- 計測データの保管先は [orcarouter-model-cost-measurements.md](orcarouter-model-cost-measurements.md)。

## 却下した案

### 案A: コードを変更せず、`.env` に `ORCAROUTER_VISION_MODEL` を明示するだけにする

一度設定すれば同じ穴は塞がるため、提出まで1日という状況では合理的だった。実際に一度はこの案を採る方針で合意しかけた。

却下した理由は、設定が正しいことを**確認する手段が残らない**ため。`.env` は追跡対象外なので、あとから「どの構成で計測したか」を記事やデモで示す根拠にならない。計測データを記事の主要な根拠にする方針を採った以上、構成が実行ログに残ることの価値が上回ると判断した。

### 案B': 危険な構成のときに起動を失敗させる

構成ミスを確実に防げるが、ルーターの候補モデルをすべて画像対応にしている構成まで拒否してしまう。アプリ側からは候補モデルの一覧を知ることができず、安全かどうかを正しく判定できないため採用しない。

### 案C: 目次画像の宛先から `ORCAROUTER_MODEL` の引き継ぎ自体を廃止する

確実ではあるが、既存の動作を変える破壊的変更になる。`AGENT_MODE=llm` で `ORCAROUTER_VISION_MODEL` を設定していない既存の利用者が、目次画像機能を静かに失う。警告で足りる問題に対して影響が大きすぎる。

## 後で必ず検討・実装すること

- レスポンスヘッダー `X-Orca-Resolved-Model` / `X-Orca-Fallback-Model` / `X-Orca-Fallback-Level` を読み、**実際に応答したモデル**を `llm_calls` に記録する。現状はリクエストで指定したモデル名しか残らないため、`orcarouter/auto` やルーターが裏で何を選んだか追跡できない。
- `X-Orca-Request-Id` を保存し、`GET /v1/generation` から確定請求額を取得して `llm_calls.billed_amount` を埋める。現在は常に NULL で、[evaluation.md 第6節](../../decisions/evaluation.md) が求める「見積額と確定請求額の区別」を満たせていない。
- `extra_body.models` と `extra_body.route="fallback"` による別ベンダーへのフェイルオーバー。現在の再試行は同じモデルへ時間をおいて行うだけで、プロバイダ障害時に別系統へ逃がせない。
- 目次検索モデルを有効にするかどうかの最終判断。現時点では未設定（機能オフ）のまま。

## push までの運用

- コミットと push はユーザーと確認してから行う。
