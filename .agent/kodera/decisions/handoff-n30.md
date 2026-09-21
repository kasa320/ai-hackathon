# 引き継ぎ：計測の試行数を30件に増やす

作成: 2026-09-22 ／ 提出期限 15:00 JST

## いま動いているもの

**ポート 24690 で計画側の90試行（3構成 × 30試行）が実行中。** 止めるか引き継ぐかを最初に決める。

```
コマンド: python scripts/measure.py --runs 30 --only planner --cases E01 \
            --timeout 180 --port 24690 --out-dir .agent/kodera/research/planner-n30 \
            --configs auto,gpt-5-mini,gemini-2.5-flash
出力:     .agent/kodera/research/planner-n30/measurements-<日時>.csv
見込み:   60〜90分、費用 $0.5〜2
```

引き継ぐ場合、**このプロセスを二重に起動しないこと。** 過去に同じポートで2つ走らせて
データを1回汚染している。確認と停止は次のとおり。

```sh
curl -s -m 2 localhost:24690/api/health -o /dev/null -w "%{http_code}\n"   # 200 なら稼働中

# 止めるとき（PowerShell の Get-Process + CommandLine では落ちない。CimInstance を使う）
powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { \$_.CommandLine -like '*measure.py*' } | ForEach-Object { Stop-Process -Id \$_.ProcessId -Force }; Get-NetTCPConnection -LocalPort 24690 -State Listen -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id \$_.OwningProcess -Force }"
```

`go run` が生成する `server.exe` はコマンドラインに `cmd/server` を含まないため、
**ポート所有者を直接落とす**必要がある。

## なぜ30件に増やすのか

これまでの計測はすべて **n=3**。分布も成功率も語れない。特に次の主張が裏づけ不足。

**「`auto` は最悪値が読めない」** — 根拠は解釈側で1回 `claude-opus-5`（$0.046776）を
引いたという単発の観測だけ。計画側で30回引けば、それが起きるのか分かる。

現状の n=3 の値（参考、これを置き換える）:

| 構成 | 成功 | 所要 | 平均費用 | 最小〜最大 |
| --- | ---: | --- | --- | --- |
| `orcarouter/auto` | 3/3 | 11〜59秒 | $0.00469 | $0.00076〜0.01248 |
| `openai/gpt-5-mini` | 3/3 | 16〜20秒 | $0.00494 | $0.0046〜0.0056 |
| `google/gemini-2.5-flash` | 3/3 | 16〜31秒 | $0.00502 | $0.0042〜0.0063 |

## やってほしいこと

### 1. 計画側 90試行を完走させる（実行中）

終わったら、平均だけでなく**中央値・最大値・標準偏差**を出す。
`auto` が引いたモデルの**出現頻度**も数える（`解決モデル` 列）。

### 2. 解釈側も30件に増やす

計画側の完了後に実行する（**ポートが空いてから**）。

```sh
set -a && . ./.env && set +a
PYTHONIOENCODING=utf-8 python scripts/measure.py --runs 30 --only interpreter \
  --cases E09,E13 --timeout 40 --port 24690 \
  --out-dir .agent/kodera/research/extract-n30 \
  --configs auto,flash,gpt-5-mini
```

`flash-lite` は二極化と503で候補から外したので含めなくてよい。
`marunage-extract` はコンソール設定を変更予定のため、変更後に測る。

### 3. 数値の差し替え

n=30 の結果で次を更新する。**n=3 の値が残っていると記事と食い違う。**

- `.agent/kodera/decisions/named-router.md`（根拠1の表、言えること・言えないことの表）
- `.agent/kodera/decisions/orcarouter-model-cost-measurements.md`（第3〜4節）
- `.agent/kodera/pitch/article-draft.md`（発見4の表、auto比較の節、まとめの表）

「試行数は各3回。統計的な差は主張できない」という但し書きを外せるかも判断する。

## 踏んではいけない落とし穴（すべて実際に踏んだ）

### 合格線を根拠なく決めない

計画の合格線を60秒にしたところ `auto` が 0/3 になり、「`auto` は使えない」と誤った結論を出しかけた。
アプリの既定値である180秒で取り直すと **3/3 成功**。59秒・58秒という値は60秒の直前で、
**線がちょうど結果を分ける位置にあった。**

計画の実際の締切は「回答期間（`MinResponseWindow` = 30分）を確保できるうちに案ができていること」で、
分〜十数分の単位。解釈は Discord の1往復が60秒で打ち切られるという仕様上の制約がある。

### 打ち切られた試行の所要秒を「実測値」として使わない

合格線ちょうどの値（20.0 / 40.0 / 180.0 / 240.2）は「その値以上」の意味しかない。
**中央値も「60秒超が0件」も、打ち切りの内側でしか言えない。**

### 同じ入力を連投して独立試行と見なさない

Gemini のスキーマ検証で、同じスキーマを5回連続で投げたところ、1回目の結果がキャッシュ経由で
残り4回に伝播し、**実質1試行が5倍に増幅**されていた。結論を誤りかけた。
なお Gemini は `description` を無視してキャッシュキーを作るため、説明文を変えても無効化できない。

### 計測器の判定を疑う

判定ロジックに2件の誤りがあった。どちらも「失敗が多い」方向で、実装が悪いという
誤った結論に傾きかけた。

1. E13 の判定が `SELECT COUNT(*) FROM tasks WHERE decision IS NOT NULL > 0` だったが、
   `replan_demo` のシードが回答済みタスクを3件作るため、**LLM を呼ぶ前から条件を満たしていた**。
   → 解釈の前後で差分を取る形に修正済み。
2. モデルが「扱えない依頼」と判断するとサーバーは HTTP 409 を返すが、200 以外をすべて失敗に
   していた。E13 ではこれが**期待動作**。→ 「安全拒否」として別に数えるよう修正済み。

### サーバーの停止を確認する

「停止した」と報告した計測が動き続け、新しい計測とポートを奪い合ってデータを汚染した
（`.trash/measurements-contaminated-20260921/`）。**停止後は必ず `/api/health` で確認する。**

## 計測スクリプトの引数

```
--runs N          1構成あたりの試行数
--only            planner / interpreter
--configs         構成名をカンマ区切り（PLANNER_CONFIGS / INTERPRETER_CONFIGS の第1要素）
--cases           E01 / E09 / E13
--timeout N       LLM呼び出し1回の上限秒。製品要件に合わせた合格線として使う
--port N          待ち受けポート
--out-dir PATH    CSV と作業ファイルの出力先
```

CSV は実行ごとに新しい日時名で作られる。既存結果は上書きされない。
失敗した試行も削除しない。費用が取得できなければ「不明」と記録し、0 として扱わない。

## 未解決の問い

**Named Router の受け皿（Fallback）が一度も発動していない。** 79試行すべてで
`fallback_level` = 0。ルーターを使う根拠が受け皿だけになっているのに、それが未検証。

検証案: 検証用ルーターを作り、許可モデルに確実に失敗するもの（503が続いている
`anthropic/claude-haiku-4.5` など）を置き、フォールバックに `gemini-2.5-flash` を指定して呼ぶ。
`fallback_level` が1以上になり `resolved_model` が受け皿側になれば発動確認。

発動しないなら、ルーターを使う理由がなくなる。直指定に戻すか、`extra_body.models` で
リクエスト単位のフォールバックを実装するかの判断になる（後者は `agent/llm.go` の
`ChatRequest` に `extra_body` を足すコード変更が必要）。

## リポジトリの状態

- ブランチ `feat/orcarouter-optimization`（push 済み、ahead 1 はこのファイル）
- 作業ツリーはクリーン。テストは全パッケージ成功
- `.env` はまだ最終構成に更新していない
- **コンソールのルーター2本は旧構成のまま**（アダプティブゲート＋`flash-lite`）。
  最終構成は `named-router.md` の「コンソールへの入力値」にある
- 残クレジット: プロモ $20 に対して使用 $0.53（2.6%）
