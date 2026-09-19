-- テーブル定義。起動時に毎回適用するため、CREATE TABLE IF NOT EXISTS で書く。
-- データモデル確定後に追加する。

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
