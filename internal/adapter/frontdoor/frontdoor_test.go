package frontdoor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func backend(t *testing.T, label string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(label + " " + r.URL.Path))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, h http.Handler, path string) (int, string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body), rec.Header().Get("X-Served-By")
}

func TestRoutesSplitTraffic(t *testing.T) {
	orig := backend(t, "original")
	rew := backend(t, "rewrite")
	h, err := New([]domain.Route{{PathPrefix: "/api/new", Target: domain.TargetRewrite}}, orig.URL, rew.URL)
	if err != nil {
		t.Fatal(err)
	}
	code, body, who := get(t, h, "/api/new/x")
	if code != 200 || body != "rewrite /api/new/x" || who != domain.TargetRewrite {
		t.Fatalf("rewrite route: %d %q %q", code, body, who)
	}
	code, body, who = get(t, h, "/api/old")
	if code != 200 || body != "original /api/old" || who != domain.TargetOriginal {
		t.Fatalf("original route: %d %q %q", code, body, who)
	}
}

func TestRewriteDownIsBadGatewayNotSilentFallback(t *testing.T) {
	orig := backend(t, "original")
	rew := backend(t, "rewrite")
	rewURL := rew.URL
	rew.Close() // rewrite is down
	h, err := New([]domain.Route{{PathPrefix: "/api/new", Target: domain.TargetRewrite}}, orig.URL, rewURL)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := get(t, h, "/api/new/x")
	if code != http.StatusBadGateway {
		t.Fatalf("a down rewrite must be 502, got %d (%q): silent fallback hides failure", code, body)
	}
	if strings.Contains(body, "original") {
		t.Fatal("must not answer from the original when the rewrite was asked for")
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	orig := backend(t, "o")
	cases := map[string]struct {
		routes  []domain.Route
		orig    string
		rewrite string
	}{
		"rewrite route without rewrite url": {[]domain.Route{{PathPrefix: "/a", Target: domain.TargetRewrite}}, orig.URL, ""},
		"bad original scheme":               {nil, "ftp://x", ""},
		"bad route":                         {[]domain.Route{{PathPrefix: "a", Target: domain.TargetRewrite}}, orig.URL, "http://x"},
		"missing host":                      {nil, "http://", ""},
	}
	for name, c := range cases {
		if _, err := New(c.routes, c.orig, c.rewrite); err == nil {
			t.Fatalf("%s: want error", name)
		}
	}
}

func TestQueryAndMethodForwarded(t *testing.T) {
	var gotMethod, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotQuery = r.Method, r.URL.RawQuery
	}))
	t.Cleanup(srv.Close)
	h, err := New(nil, srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/p?a=1&b=2", nil))
	if gotMethod != "PUT" || gotQuery != "a=1&b=2" {
		t.Fatalf("method/query lost: %s %q", gotMethod, gotQuery)
	}
}
