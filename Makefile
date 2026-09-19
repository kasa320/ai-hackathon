# .env があれば読み込み、環境変数として渡す。
-include .env
export

BACKEND := src/backend

.PHONY: dev build test fmt vet db-reset

## dev: サーバーをビルドして起動する（http://localhost:8080）
dev: build
	./bin/server

## build: サーバーと評価ツールをビルドする
build:
	go -C $(BACKEND) build -o ../../bin/server ./cmd/server
	go -C $(BACKEND) build -o ../../bin/eval ./cmd/eval

## test: バックエンドのテストを実行する
test:
	go -C $(BACKEND) test ./...

## fmt: Go のコードを整形する
fmt:
	go -C $(BACKEND) fmt ./...

## vet: Go のコードを静的検査する
vet:
	go -C $(BACKEND) vet ./...

## db-reset: ローカルの DB を削除する（次回起動時に作り直される）
db-reset:
	rm -f data/app.db data/app.db-wal data/app.db-shm
