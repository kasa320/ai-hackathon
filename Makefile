# .env があれば読み込み、環境変数として渡す。
-include .env
export

BACKEND := src/backend

.PHONY: dev build test test-e2e fmt vet db-reset

## dev: サーバーをビルドして起動する（http://localhost:8080）
dev: build
	./bin/server

## build: サーバーと評価ツールをビルドする
build:
	go -C $(BACKEND) build -o ../../bin/server ./cmd/server
	go -C $(BACKEND) build -o ../../bin/eval ./cmd/eval

## test: バックエンドのテスト（単体・結合）をすべて実行する
test:
	go -C $(BACKEND) test ./...

## test-e2e: HTTP 経由の結合テスト（評価ケース E01〜E12・目次の取得）だけを実行する
test-e2e:
	go -C $(BACKEND) test ./tests/e2e/ -v -count=1

## fmt: Go のコードを整形する
fmt:
	go -C $(BACKEND) fmt ./...

## vet: Go のコードを静的検査する
vet:
	go -C $(BACKEND) vet ./...

## db-reset: ローカルの DB を削除する（次回起動時に作り直される）
db-reset:
	rm -f data/app.db data/app.db-wal data/app.db-shm
