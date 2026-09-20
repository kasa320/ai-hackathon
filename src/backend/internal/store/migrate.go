package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// meta に入れるスキーマ版の鍵。値は適用済みの最大の版番号（10進の文字列）。
const schemaVersionKey = "schema_version"

// fkOffDirective はテーブルを作り直す版の先頭に書く指示。
// SQLite では列の削除・型変更・制約変更ができず、新しい表へ移し替える手順になる。
// その間だけ外部キー検査を止め、コミット前に PRAGMA foreign_key_check で壊れていないかを確かめる。
const fkOffDirective = "-- foreign_keys: off"

// migration は migrations/ に置いた1つの版。
// ファイル名は <4桁以上の版番号>_<説明>.sql とし、版番号の昇順で適用する。
type migration struct {
	version int
	name    string
	sql     string
	noFK    bool
}

// migrate は未適用の版を順に適用する。
//
// 1つの版は1トランザクションで適用し、同じトランザクションで meta の版番号も更新する。
// 途中で失敗したらその版の変更だけを巻き戻すので、原因を直せば次の起動で同じ版からやり直せる。
//
// 版番号を持たない DB（マイグレーション導入前に作ったもの、または新規）は版 0 として扱い、
// 0001 から適用する。0001 は再適用しても何も起こらないため、既存の DB はそのまま引き継がれる。
func migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	return applyMigrations(ctx, db, migrations)
}

// applyMigrations は渡された版のうち未適用のものを順に適用する。migrate から呼ぶ。
func applyMigrations(ctx context.Context, db *sql.DB, migrations []migration) error {
	if len(migrations) == 0 {
		return errors.New("マイグレーションが1つもありません")
	}
	// 版番号は meta に置くので、版番号を読む前にこの表だけは用意しておく。定義は 0001 と同じ。
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		return fmt.Errorf("meta の作成: %w", err)
	}
	current, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if latest := migrations[len(migrations)-1].version; current > latest {
		return fmt.Errorf("DB のスキーマ版 %d はこのプログラムが知っている版 %d より新しいです。新しいプログラムで起動してください", current, latest)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// schemaVersion は適用済みの版番号を返す。記録がなければ 0。
func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var raw string
	err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", schemaVersionKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("スキーマ版の取得: %w", err)
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("スキーマ版が読めません %q: %w", raw, err)
	}
	return v, nil
}

// SchemaVersion は適用済みのスキーマ版を返す（確認・調査用）。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return schemaVersion(ctx, s.db)
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) (err error) {
	// 外部キー検査の切り替えはトランザクションの中では効かないため、先に設定する。
	// 接続は1本に固定しているので、この設定は確実に同じ接続へ効く。
	if m.noFK {
		if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return fmt.Errorf("%s: 外部キー検査の停止: %w", m.name, err)
		}
		// 戻せなかったときは、検査が止まったまま業務処理を始めないようにエラーにする。
		defer func() {
			if _, e := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); e != nil && err == nil {
				err = fmt.Errorf("%s: 外部キー検査を戻せません: %w", m.name, e)
			}
		}()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: トランザクション開始: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%s: 適用: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
		schemaVersionKey, strconv.Itoa(m.version)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%s: 版番号の記録: %w", m.name, err)
	}
	if m.noFK {
		if err := checkForeignKeys(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("%s: %w", m.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: コミット: %w", m.name, err)
	}
	return nil
}

// checkForeignKeys は参照先を失った行がないかを調べる。外部キー検査を止めて適用した版の最後に呼ぶ。
func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("外部キーの検査: %w", err)
	}
	defer rows.Close()
	var broken []string
	for rows.Next() {
		// 列は 行のある表・rowid・参照先の表・何番目の外部キーか。
		var table, parent sql.NullString
		var rowid, fkID sql.NullInt64
		if err := rows.Scan(&table, &rowid, &parent, &fkID); err != nil {
			return fmt.Errorf("外部キーの検査: %w", err)
		}
		broken = append(broken, table.String+" -> "+parent.String)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("外部キーの検査: %w", err)
	}
	if len(broken) > 0 {
		return fmt.Errorf("外部キーが壊れた行があります: %s", strings.Join(broken, ", "))
	}
	return nil
}

// loadMigrations は埋め込んだ SQL を版番号の昇順で読み込む。
// ファイル名が規則に合わない、版番号が重複する、版番号が 1 未満のときはエラーにする。
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("マイグレーションの読み込み: %w", err)
	}
	out := make([]migration, 0, len(entries))
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[m.version]; dup {
			return nil, fmt.Errorf("マイグレーションの版番号 %d が重複しています: %s と %s", m.version, prev, m.name)
		}
		seen[m.version] = m.name
		body, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("%s の読み込み: %w", m.name, err)
		}
		m.sql = string(body)
		m.noFK = strings.HasPrefix(strings.TrimSpace(m.sql), fkOffDirective)
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func parseMigrationName(name string) (migration, error) {
	digits, rest, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
	if !ok || digits == "" || rest == "" {
		return migration{}, fmt.Errorf("マイグレーションのファイル名が規則に合いません: %s（<版番号>_<説明>.sql）", name)
	}
	v, err := strconv.Atoi(digits)
	if err != nil {
		return migration{}, fmt.Errorf("マイグレーションの版番号が読めません: %s", name)
	}
	if v < 1 {
		return migration{}, fmt.Errorf("マイグレーションの版番号は1以上にしてください: %s", name)
	}
	return migration{version: v, name: name}, nil
}
