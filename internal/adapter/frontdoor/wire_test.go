package frontdoor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/routesfile"
	"github.com/go-anything/go-anything/internal/domain"
)

func setRoutes(t *testing.T, path, target string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fmtRoutes(target)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Set the mtime explicitly: filesystems can have coarse timestamps, and the test must not depend on luck.
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func fmtRoutes(target string) string {
	return `{"routes":[{"unit":"orders","method":"*","path_prefix":"/orders","target":"` + target + `"}]}`
}

func servedBy(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/orders/1", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestWireSwitchesUnitWithoutRestart(t *testing.T) {
	orig := backend(t, "original")
	rew := backend(t, "rewrite")
	path := filepath.Join(t.TempDir(), "routes.json")
	base := time.Now().Add(-time.Hour)
	setRoutes(t, path, domain.TargetRewrite, base)

	h, err := NewFromFile(path, orig.URL, rew.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := servedBy(t, h); got != "rewrite /orders/1" {
		t.Fatalf("start on rewrite: got %q", got)
	}
	// wire the unit back to the original, through the same code path the CLI uses
	if _, err := routesfile.Set(path, "orders", domain.TargetOriginal); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := servedBy(t, h); got != "original /orders/1" {
		t.Fatalf("after wire, the next request must go to the original: got %q", got)
	}
}

func TestInvalidEditKeepsPreviousRoutes(t *testing.T) {
	orig := backend(t, "original")
	rew := backend(t, "rewrite")
	path := filepath.Join(t.TempDir(), "routes.json")
	base := time.Now().Add(-time.Hour)
	setRoutes(t, path, domain.TargetRewrite, base)
	h, err := NewFromFile(path, orig.URL, rew.URL)
	if err != nil {
		t.Fatal(err)
	}
	// a broken edit: unknown target
	if err := os.WriteFile(path, []byte(`{"routes":[{"path_prefix":"/orders","target":"nonsense"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := servedBy(t, h); got != "rewrite /orders/1" {
		t.Fatalf("a bad edit must keep the last good routes: got %q", got)
	}
}

func TestNewFromFileRequiresValidStart(t *testing.T) {
	orig := backend(t, "original")
	path := filepath.Join(t.TempDir(), "routes.json")
	os.WriteFile(path, []byte(`{"routes":[{"path_prefix":"/x","target":"rewrite"}]}`), 0o600)
	if _, err := NewFromFile(path, orig.URL, ""); err == nil {
		t.Fatal("a rewrite route without a rewrite URL must be refused at start")
	}
	if _, err := NewFromFile(filepath.Join(t.TempDir(), "missing.json"), orig.URL, ""); err == nil {
		t.Fatal("a missing routes file must be an error")
	}
}
