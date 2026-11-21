package dbsnap

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/domain"
)

// The PostgreSQL tests need a real server. Point GA_TEST_PG_ADMIN_DSN at a scratch database
// (a URL such as postgres://user:pw@127.0.0.1:5432/db?sslmode=disable) with a superuser that can
// create schemas and roles. Without it the tests are skipped, and that is reported, not hidden.
const adminEnv = "GA_TEST_PG_ADMIN_DSN"

func adminDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(adminEnv)
	if dsn == "" {
		t.Skipf("%s not set: PostgreSQL behaviour is untested in this run", adminEnv)
	}
	return dsn
}

// scratch creates a unique schema, runs setup statements as the admin, and drops everything after.
func scratch(t *testing.T, setup ...string) (admin *sql.DB, schema string) {
	t.Helper()
	dsn := adminDSN(t)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema = fmt.Sprintf("ga_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		admin.Close()
	})
	for _, s := range setup {
		if _, err := admin.Exec(strings.ReplaceAll(s, "SCHEMA", schema)); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	return admin, schema
}

func TestPostgresSnapshotReadsRowsAndNumerics(t *testing.T) {
	_, schema := scratch(t,
		`CREATE TABLE SCHEMA.orders (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, status text NOT NULL, amount numeric(10,2) NOT NULL)`,
		`INSERT INTO SCHEMA.orders (status, amount) VALUES ('new', 12.5), ('paid', 7)`,
	)
	s, err := OpenPostgres(adminDSN(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tbl, err := s.Snapshot(schema + ".orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("want 2 rows, got %+v", tbl.Rows)
	}
	found := false
	for _, r := range tbl.Rows {
		if r["status"] == "new" && r["amount"] == "12.50" {
			found = true
		}
	}
	if !found {
		t.Fatalf("numeric value not read as expected: %+v", tbl.Rows)
	}
}

func TestPostgresPrimaryKeyInKeyOrder(t *testing.T) {
	_, schema := scratch(t,
		`CREATE TABLE SCHEMA.links (a int, b int, PRIMARY KEY (b, a))`,
	)
	s, err := OpenPostgres(adminDSN(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pk, err := s.PrimaryKey(schema + ".links")
	if err != nil {
		t.Fatal(err)
	}
	if len(pk) != 2 || pk[0] != "b" || pk[1] != "a" {
		t.Fatalf("composite key must keep definition order (b, a), got %v", pk)
	}
}

func TestPostgresSessionIsReadOnly(t *testing.T) {
	_, schema := scratch(t,
		`CREATE TABLE SCHEMA.t (id int PRIMARY KEY)`,
	)
	s, err := OpenPostgres(adminDSN(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The server itself refuses the write, even though this is the superuser.
	if _, err := s.db.Exec("INSERT INTO " + schema + ".t (id) VALUES (1)"); err == nil {
		t.Fatal("a write through the snapshotter must be refused by the server")
	}
}

func TestPostgresMissingTableIsError(t *testing.T) {
	scratch(t)
	s, err := OpenPostgres(adminDSN(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.PrimaryKey("no_such_table_ga"); err == nil {
		t.Fatal("a missing table must be an error, not an empty key")
	}
	if _, err := s.Snapshot("no_such_table_ga"); err == nil {
		t.Fatal("a missing table must be an error, not an empty snapshot")
	}
}

func TestPostgresRejectsUnsafeNames(t *testing.T) {
	s, err := OpenPostgres(adminDSN(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{`orders"; DROP TABLE x; --`, "a.b.c", "x y", ""} {
		if _, err := s.Snapshot(name); err == nil {
			t.Fatalf("unsafe name %q must be refused before reaching SQL", name)
		}
	}
}

// RLS: the snapshot must see exactly what the application's role sees for the tenant it sets.
func TestPostgresTenantRowLevelSecurityIsolatesSnapshots(t *testing.T) {
	role := fmt.Sprintf("ga_rls_%d", time.Now().UnixNano())
	admin, schema := scratch(t,
		`CREATE TABLE SCHEMA.tasks (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, tenant_id bigint NOT NULL, title text NOT NULL)`,
		`ALTER TABLE SCHEMA.tasks ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY tenant_isolation ON SCHEMA.tasks USING (tenant_id = (SELECT NULLIF(current_setting('app.current_tenant_id', true), '')::bigint))`,
		`INSERT INTO SCHEMA.tasks (tenant_id, title) VALUES (1, 'a'), (1, 'b'), (2, 'c')`,
	)
	t.Cleanup(func() { admin.Exec("DROP ROLE IF EXISTS " + role) })
	if _, err := admin.Exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'ga_rls_pw' NOSUPERUSER NOBYPASSRLS", role)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, role)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(fmt.Sprintf("GRANT SELECT ON %s.tasks TO %s", schema, role)); err != nil {
		t.Fatal(err)
	}
	appDSN := appURL(t, adminDSN(t), role, "ga_rls_pw")

	cases := map[string]int{"1": 2, "2": 1, "": 0}
	for tenant, want := range cases {
		s, err := OpenPostgres(appDSN, "app.current_tenant_id", tenant)
		if err != nil {
			t.Fatal(err)
		}
		tbl, err := s.Snapshot(schema + ".tasks")
		s.Close()
		if err != nil {
			t.Fatalf("tenant %q: %v", tenant, err)
		}
		if len(tbl.Rows) != want {
			t.Fatalf("tenant %q: want %d rows (fail closed for empty), got %d: %+v", tenant, want, len(tbl.Rows), tbl.Rows)
		}
		for _, r := range tbl.Rows {
			if r["tenant_id"] != tenant {
				t.Fatalf("tenant %q saw another tenant's row: %+v", tenant, r)
			}
		}
	}
}

// Without any tenant setting configured, RLS still fails closed: no rows from a tenant table.
func TestPostgresNoTenantSettingSeesNoTenantRows(t *testing.T) {
	role := fmt.Sprintf("ga_rls_%d", time.Now().UnixNano())
	admin, schema := scratch(t,
		`CREATE TABLE SCHEMA.tasks (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, tenant_id bigint NOT NULL)`,
		`ALTER TABLE SCHEMA.tasks ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY tenant_isolation ON SCHEMA.tasks USING (tenant_id = (SELECT NULLIF(current_setting('app.current_tenant_id', true), '')::bigint))`,
		`INSERT INTO SCHEMA.tasks (tenant_id) VALUES (1)`,
	)
	t.Cleanup(func() { admin.Exec("DROP ROLE IF EXISTS " + role) })
	admin.Exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'ga_rls_pw' NOSUPERUSER NOBYPASSRLS", role))
	admin.Exec(fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, role))
	admin.Exec(fmt.Sprintf("GRANT SELECT ON %s.tasks TO %s", schema, role))
	s, err := OpenPostgres(appURL(t, adminDSN(t), role, "ga_rls_pw"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tbl, err := s.Snapshot(schema + ".tasks")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 0 {
		t.Fatalf("with no tenant set, RLS must return no rows, got %+v", tbl.Rows)
	}
}

// appURL returns the admin URL with another user and password, without ever printing either.
func appURL(t *testing.T, admin, user, pw string) string {
	t.Helper()
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("admin DSN is not a URL: %v", err)
	}
	u.User = url.UserPassword(user, pw)
	return u.String()
}

// The watcher must work on PostgreSQL too: changes between drains become effects.
func TestPostgresWatcherReportsChanges(t *testing.T) {
	admin, schema := scratch(t,
		`CREATE TABLE SCHEMA.orders (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, status text NOT NULL)`,
		`INSERT INTO SCHEMA.orders (status) VALUES ('new')`,
	)
	s, err := OpenPostgres(adminDSN(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := NewWatcher(s, []string{schema + ".orders"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(fmt.Sprintf("INSERT INTO %s.orders (status) VALUES ('paid')", schema)); err != nil {
		t.Fatal(err)
	}
	got := w.Drain()
	if len(got) != 1 || got[0].Kind != domain.EffectDB || got[0].Op != "insert" {
		t.Fatalf("the insert must be one db effect: %+v", got)
	}
	if !strings.Contains(got[0].Body, "status=paid") || strings.Contains(got[0].Body, "id=") {
		t.Fatalf("insert body must keep content and drop the generated key: %q", got[0].Body)
	}
}

// A reset script has several statements; the writable connection must run all of them.
func TestPostgresResetScriptRunsEveryStatement(t *testing.T) {
	_, schema := scratch(t,
		`CREATE TABLE SCHEMA.orders (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, status text NOT NULL)`,
		`INSERT INTO SCHEMA.orders (status) VALUES ('old')`,
	)
	w, err := OpenPostgresWritable(adminDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.ExecScript(fmt.Sprintf("DELETE FROM %s.orders; INSERT INTO %s.orders (status) VALUES ('seed');", schema, schema)); err != nil {
		t.Fatal(err)
	}
	r, err := OpenPostgres(adminDSN(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tbl, err := r.Snapshot(schema + ".orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) != 1 || tbl.Rows[0]["status"] != "seed" {
		t.Fatalf("multi-statement reset failed: %+v", tbl.Rows)
	}
}
