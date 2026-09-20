# 起動方法

必要なもの：Go 1.25 以上、make。フロントエンドにビルドは要りません。

## 準備

```sh
cp .env.example .env
```

## 起動

```sh
make dev
```

バックエンドが起動し、同じプロセスが `src/frontend/` を配信します。<http://localhost:24680> を開きます。フロントエンド用のサーバーは別に立てません。

## デモ・開発モード

`.env` に次を設定すると、画面上部にデモ用の帯（人物の切り替え・時計を進める・初期データの投入）が出て、`/api/dev/*` が有効になります。

```sh
DEV_MODE=1
```

初期データは画面の帯から入れるか、直接叩きます。

```sh
curl -X POST http://localhost:24680/api/dev/seed \
  -H 'Content-Type: application/json' -H 'Origin: http://localhost:24680' \
  -d '{"scenario":"replan_demo"}'
```

`replan_demo` は初回計画が確定済みの状態、`initial_demo` は開催回を登録した直後の状態です。

## 実モデルを使う

既定の `AGENT_MODE=fake` は LLM を呼ばず、規則だけで案を作ります。OrcaRouter を使うときは `.env` に設定します。

```sh
AGENT_MODE=llm
ORCAROUTER_API_KEY=...
```

## その他のコマンド

```sh
make build      # サーバーと評価ツールをビルドする
make test       # バックエンドのテストをすべて実行する
make test-e2e   # HTTP 経由の結合テストだけ実行する
make fmt        # Go のコードを整形する
make vet        # Go のコードを静的検査する
make db-reset   # ローカルのDBを削除する（次回起動時に作り直される）
```

DBはスキーマを流すだけでマイグレーションをしません。テーブル定義を変えたら `make db-reset` してから起動してください。

## 別のポートで起動する

```sh
make dev ADDR=:8090 PUBLIC_BASE_URL=http://localhost:8090
```

`.env` の値が優先されるため、`ADDR=:8090 make dev` のように前に置く書き方では効きません。`PUBLIC_BASE_URL` は通知に載せるリンクと Origin 検証の基点なので、`ADDR` と合わせます。
