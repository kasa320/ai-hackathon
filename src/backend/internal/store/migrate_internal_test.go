package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var bg = context.Background()

// openRaw は migrate を通さずに DB を開く。マイグレーション自体を試すために使う。
func openRaw(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// マイグレーション導入より前に作った DB（0001 の形で、版番号の記録がない）を、
// 中身を保ったまま引き継ぎ、残りの版を適用できる。
func TestMigrateAdoptsDatabaseWithoutVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	// 導入前の DB を再現する：0001 だけを直接流し、版番号は記録しない。
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.ExecContext(bg, ms[0].sql); err != nil {
		t.Fatal(err)
	}
	const userID = "usr_000000000000000000000001"
	if _, err := raw.ExecContext(bg,
		"INSERT INTO users (id, discord_user_id, display_name, created_at) VALUES (?, ?, ?, ?)",
		userID, "100000000000000001", "A", ts(time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(bg, "SELECT value FROM meta WHERE key = 'schema_version'"); err != nil {
		t.Fatalf("0001 に meta がありません: %v", err)
	}
	raw.Close()

	st, err := Open(bg, path)
	if err != nil {
		t.Fatalf("導入前の DB を開けませんでした: %v", err)
	}
	defer st.Close()

	want := ms[len(ms)-1].version
	if v, _ := st.SchemaVersion(bg); v != want {
		t.Errorf("スキーマ版が %d（期待 %d）", v, want)
	}
	if err := st.Tx(bg, func(tx *Tx) error {
		u, err := tx.User(bg, userID)
		if err != nil {
			return err
		}
		if u.DisplayName != "A" {
			t.Errorf("表示名が %q（期待 \"A\"）", u.DisplayName)
		}
		return nil
	}); err != nil {
		t.Fatalf("既存のデータが失われました: %v", err)
	}
}

func TestParseMigrationName(t *testing.T) {
	ok := []struct {
		name    string
		version int
	}{
		{"0001_init.sql", 1},
		{"0002_add_left_at.sql", 2},
		{"0010_a_b_c.sql", 10},
	}
	for _, c := range ok {
		m, err := parseMigrationName(c.name)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if m.version != c.version {
			t.Errorf("%s: 版番号が %d（期待 %d）", c.name, m.version, c.version)
		}
	}
	for _, name := range []string{"init.sql", "0001.sql", "_init.sql", "abc_init.sql", "0000_init.sql"} {
		if _, err := parseMigrationName(name); err == nil {
			t.Errorf("%s: エラーになりませんでした", name)
		}
	}
}

// 埋め込んだマイグレーションが規則どおりに読めて、0001 から隙間なく並ぶことを確かめる。
func TestLoadMigrations(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 {
		t.Fatal("マイグレーションが読み込めていません")
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Fatalf("%s: 版番号が %d（期待 %d）。番号は1から連番にする", m.name, m.version, i+1)
		}
		if strings.TrimSpace(m.sql) == "" {
			t.Errorf("%s: 中身が空です", m.name)
		}
	}
}

func TestApplyMigrationsRunsPendingOnly(t *testing.T) {
	db := openRaw(t)
	first := []migration{{version: 1, name: "0001_init.sql", sql: "CREATE TABLE t (id TEXT PRIMARY KEY);"}}
	if err := applyMigrations(bg, db, first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(bg, "INSERT INTO t (id) VALUES ('a')"); err != nil {
		t.Fatal(err)
	}

	// 同じ版をもう一度流しても、適用済みなので何も起きない（起きれば重複作成で失敗する）。
	if err := applyMigrations(bg, db, first); err != nil {
		t.Fatalf("適用済みの版が再実行されました: %v", err)
	}

	// 新しい版だけが適用される。
	second := append(first, migration{version: 2, name: "0002_add_note.sql", sql: "ALTER TABLE t ADD COLUMN note TEXT;"})
	if err := applyMigrations(bg, db, second); err != nil {
		t.Fatal(err)
	}
	var v int
	if v, _ = schemaVersion(bg, db); v != 2 {
		t.Fatalf("スキーマ版が %d（期待 2）", v)
	}
	var note sql.NullString
	if err := db.QueryRowContext(bg, "SELECT note FROM t WHERE id = 'a'").Scan(&note); err != nil {
		t.Fatalf("既存の行が失われました: %v", err)
	}
}

// 失敗した版は丸ごと巻き戻り、版番号も上がらない。
func TestApplyMigrationsRollsBackOnError(t *testing.T) {
	db := openRaw(t)
	ms := []migration{
		{version: 1, name: "0001_init.sql", sql: "CREATE TABLE t (id TEXT PRIMARY KEY);"},
		{version: 2, name: "0002_broken.sql", sql: "CREATE TABLE ok (id TEXT); THIS IS NOT SQL;"},
	}
	err := applyMigrations(bg, db, ms)
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "0002_broken.sql") {
		t.Errorf("エラーに版のファイル名が含まれていません: %v", err)
	}
	if v, _ := schemaVersion(bg, db); v != 1 {
		t.Errorf("スキーマ版が %d（期待 1）", v)
	}
	// 失敗した版が途中まで作った表は残っていない。
	var n int
	if err := db.QueryRowContext(bg, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'ok'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("失敗した版の変更が残っています")
	}
}

// 知らない版まで進んだ DB は、取り違えを防ぐために開かずにエラーにする。
func TestApplyMigrationsRejectsNewerDatabase(t *testing.T) {
	db := openRaw(t)
	ms := []migration{
		{version: 1, name: "0001_init.sql", sql: "CREATE TABLE t (id TEXT PRIMARY KEY);"},
		{version: 2, name: "0002_add_note.sql", sql: "ALTER TABLE t ADD COLUMN note TEXT;"},
	}
	if err := applyMigrations(bg, db, ms); err != nil {
		t.Fatal(err)
	}
	err := applyMigrations(bg, db, ms[:1])
	if err == nil {
		t.Fatal("エラーになりませんでした")
	}
	if !strings.Contains(err.Error(), "新しい") {
		t.Errorf("想定と違うエラー: %v", err)
	}
}

// テーブルを作り直す版では外部キー検査を止め、参照が壊れていればコミットしない。
func TestApplyMigrationsChecksForeignKeys(t *testing.T) {
	setup := migration{version: 1, name: "0001_init.sql", sql: `
		CREATE TABLE parent (id TEXT PRIMARY KEY);
		CREATE TABLE child (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES parent(id));
		INSERT INTO parent (id) VALUES ('p');
		INSERT INTO child (id, parent_id) VALUES ('c', 'p');`}

	t.Run("参照が保たれていれば適用できる", func(t *testing.T) {
		db := openRaw(t)
		rebuild := migration{version: 2, name: "0002_rebuild.sql", noFK: true, sql: fkOffDirective + `
			CREATE TABLE parent_new (id TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '');
			INSERT INTO parent_new (id) SELECT id FROM parent;
			DROP TABLE parent;
			ALTER TABLE parent_new RENAME TO parent;`}
		if err := applyMigrations(bg, db, []migration{setup, rebuild}); err != nil {
			t.Fatal(err)
		}
		if v, _ := schemaVersion(bg, db); v != 2 {
			t.Fatalf("スキーマ版が %d（期待 2）", v)
		}
		// 外部キー検査が元に戻っている。
		var on int
		if err := db.QueryRowContext(bg, "PRAGMA foreign_keys").Scan(&on); err != nil {
			t.Fatal(err)
		}
		if on != 1 {
			t.Error("外部キー検査が戻っていません")
		}
	})

	t.Run("参照が壊れると巻き戻る", func(t *testing.T) {
		db := openRaw(t)
		broken := migration{version: 2, name: "0002_rebuild.sql", noFK: true, sql: fkOffDirective + `
			CREATE TABLE parent_new (id TEXT PRIMARY KEY);
			DROP TABLE parent;
			ALTER TABLE parent_new RENAME TO parent;`}
		err := applyMigrations(bg, db, []migration{setup, broken})
		if err == nil {
			t.Fatal("エラーになりませんでした")
		}
		if !strings.Contains(err.Error(), "外部キー") {
			t.Errorf("想定と違うエラー: %v", err)
		}
		if v, _ := schemaVersion(bg, db); v != 1 {
			t.Errorf("スキーマ版が %d（期待 1）", v)
		}
	})
}
