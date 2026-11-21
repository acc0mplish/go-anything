// Package dbsnap reads rows from a SQLite file or a PostgreSQL database so the effects of a case
// on stored data can be compared. Connections are read-only: this tool never writes program data.
package dbsnap

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/go-anything/go-anything/internal/domain"
)

// Dialect is the database engine a Snapshotter talks to.
type Dialect int

const (
	SQLite Dialect = iota
	Postgres
)

// identPart is one identifier: a plain name. Names are spliced into SQL (identifiers cannot be
// bound as parameters), so anything else is refused before it reaches a query.
var identPart = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Snapshotter reads named tables from one database.
type Snapshotter struct {
	db       *sql.DB
	writable bool // opened for writes (ExecScript only works then); snapshots never write
	dialect  Dialect
	tenantKV string // Postgres: setting name for the tenant, e.g. app.current_tenant_id ("" = none)
	tenantV  string // Postgres: tenant value for this connection
}

// Open opens a SQLite file read-only. The file must already exist.
func Open(path string) (*Snapshotter, error) {
	// mode=ro opens the file read-only; query_only also refuses writes through this handle.
	dsn := "file:" + path + "?mode=ro&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("dbsnap: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("dbsnap: %s is not a readable SQLite database: %w", path, err)
	}
	return &Snapshotter{db: db, dialect: SQLite}, nil
}

// OpenSQLiteWritable opens an existing SQLite file for writes. It is used only for an explicit
// reset before each case. The file must exist: a missing path is an error, never a new empty database.
func OpenSQLiteWritable(path string) (*Snapshotter, error) {
	dsn := "file:" + path + "?mode=rw"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("dbsnap: open %s for writing: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("dbsnap: %s is not a writable SQLite database: %w", path, err)
	}
	return &Snapshotter{db: db, writable: true, dialect: SQLite}, nil
}

// OpenPostgresWritable connects to PostgreSQL without the read-only session setting. The role it
// uses must be allowed to change the data a reset touches; that is the caller's responsibility.
func OpenPostgresWritable(dsn string) (*Snapshotter, error) {
	s, err := openPostgres(dsn, "", "", false)
	if err != nil {
		return nil, err
	}
	s.writable = true
	return s, nil
}

// ExecScript runs a SQL script (several statements allowed) on a writable snapshotter. It refuses
// on a read-only one, so a snapshot connection can never be used to change data by accident.
func (s *Snapshotter) ExecScript(script string) error {
	if !s.writable {
		return fmt.Errorf("dbsnap: this connection is read-only; open it writable to run a script")
	}
	if _, err := s.db.Exec(script); err != nil {
		return fmt.Errorf("dbsnap: script failed: %w", err)
	}
	return nil
}

// OpenPostgres connects to a PostgreSQL database. Every session is read-only at the server
// (default_transaction_read_only), so a stray write fails even if the code forgets a transaction.
//
// tenantSetting and tenantValue are optional. When tenantSetting is set, each snapshot runs inside
// a transaction that sets it with set_config, so row-level security sees the tenant the same way
// the application does. An empty tenantValue sets it to empty, which fails closed under the usual
// policy: the snapshot then sees no rows, and that is reported as a difference, not hidden.
func OpenPostgres(dsn, tenantSetting, tenantValue string) (*Snapshotter, error) {
	return openPostgres(dsn, tenantSetting, tenantValue, true)
}

func openPostgres(dsn, tenantSetting, tenantValue string, readOnly bool) (*Snapshotter, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("dbsnap: parse postgres address: %w", err)
	}
	if readOnly {
		cfg.RuntimeParams["default_transaction_read_only"] = "on"
	}
	cfg.RuntimeParams["application_name"] = "go-anything"
	db := stdlib.OpenDB(*cfg)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("dbsnap: connect to postgres %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
	}
	return &Snapshotter{db: db, dialect: Postgres, tenantKV: tenantSetting, tenantV: tenantValue}, nil
}

// Close releases the database.
func (s *Snapshotter) Close() error { return s.db.Close() }

// IsPostgresAddress reports whether a -db value is a PostgreSQL connection string rather than a path.
func IsPostgresAddress(v string) bool {
	return strings.HasPrefix(v, "postgres://") || strings.HasPrefix(v, "postgresql://")
}

// qualified validates a table name ("table" or "schema.table") and quotes it for the dialect.
func (s *Snapshotter) qualified(table string) (string, error) {
	parts := strings.Split(table, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("dbsnap: table name %q has too many parts", table)
	}
	for _, p := range parts {
		if !identPart.MatchString(p) {
			return "", fmt.Errorf("dbsnap: table name %q is not a plain identifier", table)
		}
	}
	if s.dialect == SQLite {
		return strings.Join(parts, "."), nil
	}
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = `"` + p + `"`
	}
	return strings.Join(quoted, "."), nil
}

// queryer is what a snapshot reads through: the database itself, or a transaction on Postgres.
type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// withRead runs fn inside a read-only transaction on Postgres, after setting the tenant if one is
// configured. SQLite has no roles or RLS, so it runs directly.
func (s *Snapshotter) withRead(fn func(q queryer) error) error {
	if s.dialect == SQLite {
		return fn(s.db)
	}
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("dbsnap: begin read transaction: %w", err)
	}
	defer tx.Rollback()
	if s.tenantKV != "" {
		// is_local=true scopes the setting to this transaction, so a pooled connection cannot carry
		// one tenant's value into the next snapshot.
		if _, err := tx.Exec("SELECT set_config($1, $2, true)", s.tenantKV, s.tenantV); err != nil {
			return fmt.Errorf("dbsnap: set tenant: %w", err)
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Snapshot reads every row of the named table. Column names come from the query result, so the
// caller does not need to know the schema.
func (s *Snapshotter) Snapshot(table string) (domain.Table, error) {
	q, err := s.qualified(table)
	if err != nil {
		return domain.Table{}, err
	}
	out := domain.Table{Name: table}
	var cols []string
	err = s.withRead(func(tx queryer) error {
		rows, err := tx.Query("SELECT * FROM " + q)
		if err != nil {
			return fmt.Errorf("dbsnap: read %s: %w", table, err)
		}
		defer rows.Close()
		cols, err = rows.Columns()
		if err != nil {
			return fmt.Errorf("dbsnap: columns of %s: %w", table, err)
		}
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return fmt.Errorf("dbsnap: scan %s: %w", table, err)
			}
			row := domain.Row{}
			for i, c := range cols {
				row[c] = vals[i].String
				if !vals[i].Valid {
					row[c] = "NULL"
				}
			}
			out.Rows = append(out.Rows, row)
		}
		return rows.Err()
	})
	if err != nil {
		return domain.Table{}, err
	}
	sort.SliceStable(out.Rows, func(i, j int) bool { return rowLess(out.Rows[i], out.Rows[j], cols) })
	return out, nil
}

// PrimaryKey returns the primary key columns of a table in key order, or nil when it has none.
func (s *Snapshotter) PrimaryKey(table string) ([]string, error) {
	q, err := s.qualified(table)
	if err != nil {
		return nil, err
	}
	if s.dialect == Postgres {
		return s.postgresPrimaryKey(q, table)
	}
	return s.sqlitePrimaryKey(q, table)
}

func (s *Snapshotter) sqlitePrimaryKey(q, table string) ([]string, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + q + ")")
	if err != nil {
		return nil, fmt.Errorf("dbsnap: schema of %s: %w", table, err)
	}
	defer rows.Close()
	type pk struct {
		pos  int
		name string
	}
	var keys []pk
	for rows.Next() {
		var cid, notnull, pkpos int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pkpos); err != nil {
			return nil, fmt.Errorf("dbsnap: schema row of %s: %w", table, err)
		}
		if pkpos > 0 {
			keys = append(keys, pk{pos: pkpos, name: name})
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].pos < keys[j].pos })
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.name)
	}
	return out, nil
}

// postgresPrimaryKey reads the primary key from the catalog, in the order the key defines.
func (s *Snapshotter) postgresPrimaryKey(q, table string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey::int2[], a.attnum)`, q)
	if err != nil {
		return nil, fmt.Errorf("dbsnap: primary key of %s: %w", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("dbsnap: primary key row of %s: %w", table, err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func rowLess(a, b domain.Row, cols []string) bool {
	for _, c := range cols {
		if a[c] != b[c] {
			return a[c] < b[c]
		}
	}
	return false
}

// Dump reads the named tables in full, for a record of their final state.
func (s *Snapshotter) Dump(tables []string) (map[string]domain.Table, error) {
	out := make(map[string]domain.Table, len(tables))
	for _, t := range tables {
		tbl, err := s.Snapshot(t)
		if err != nil {
			return nil, err
		}
		out[t] = tbl
	}
	return out, nil
}
