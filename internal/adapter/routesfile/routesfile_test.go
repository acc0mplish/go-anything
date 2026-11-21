package routesfile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRoutes(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "routes.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const twoUnits = `{"routes":[
 {"unit":"orders","method":"*","path_prefix":"/orders","target":"original"},
 {"unit":"orders","method":"*","path_prefix":"/orders-v2","target":"original"},
 {"unit":"users","method":"*","path_prefix":"/users","target":"rewrite"}]}`

func TestSetSwitchesWholeUnitOnly(t *testing.T) {
	p := writeRoutes(t, twoUnits)
	n, err := Set(p, "orders", "rewrite")
	if err != nil || n != 2 {
		t.Fatalf("both orders routes must change: n=%d err=%v", n, err)
	}
	routes, _ := Load(p)
	u := Units(routes)
	if u["orders"] != "rewrite" || u["users"] != "rewrite" {
		t.Fatalf("orders must switch and users must stay: %v", u)
	}
}

func TestSetUnknownUnitIsError(t *testing.T) {
	p := writeRoutes(t, twoUnits)
	if _, err := Set(p, "nope", "original"); err == nil {
		t.Fatal("an unknown unit must be an error, not a silent no-op")
	}
}

func TestSetRejectsBadTarget(t *testing.T) {
	p := writeRoutes(t, twoUnits)
	if _, err := Set(p, "orders", "elsewhere"); err == nil {
		t.Fatal("an unknown target must be refused")
	}
}

func TestUnitsReportsMixedRoutes(t *testing.T) {
	p := writeRoutes(t, `{"routes":[
	 {"unit":"x","path_prefix":"/a","target":"rewrite"},
	 {"unit":"x","path_prefix":"/b","target":"original"}]}`)
	routes, _ := Load(p)
	if Units(routes)["x"] != "mixed" {
		t.Fatalf("routes of one unit that disagree must show as mixed: %v", Units(routes))
	}
}

func TestSetWritesPrivateFile(t *testing.T) {
	p := writeRoutes(t, twoUnits)
	if _, err := Set(p, "users", "original"); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("routing file must stay private, mode=%v", fi.Mode().Perm())
	}
}
