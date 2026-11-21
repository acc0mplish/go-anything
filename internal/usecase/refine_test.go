package usecase

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// httpPaths returns the wire paths of the HTTP probes for the values.
func httpPaths(t *testing.T, prefix string, values []string) []domain.AppCase {
	t.Helper()
	var out []domain.AppCase
	for _, c := range ProbeCases("http", "orders", prefix, values) {
		if c.Kind == "http" {
			out = append(out, c)
		}
	}
	return out
}

func TestHTTPProbesIncludeRawEncodedDigitOne(t *testing.T) {
	var found bool
	for _, p := range httpPaths(t, "/orders/", []string{"1"}) {
		if strings.Contains(p.Path, "%31") {
			found = true
			if p.Path != "/orders/%31" {
				t.Fatalf("value 1 in raw form must be sent as /orders/%%31, got %q", p.Path)
			}
		}
	}
	if !found {
		t.Fatal("no probe sends the raw form %31 for the value 1")
	}
}

func TestHTTPProbePathsKeepRawEncodingOnWire(t *testing.T) {
	values := []string{"1", "12", "a/b", "é1", "x y", "%", "٣1", "-", ""}
	for _, p := range httpPaths(t, "/orders/", values) {
		// The request is built the way AskOriginal builds it. Its escaped path must be the probe path,
		// so the raw bytes (%31, %2F) reach the server and are not decoded on the way.
		req, err := http.NewRequest(p.Method, "http://orders.test"+p.Path, nil)
		if err != nil {
			t.Fatalf("http.NewRequest(%q): %v", p.Path, err)
		}
		if got := req.URL.EscapedPath(); got != p.Path {
			t.Fatalf("wire path changed: probe %q, request sends %q", p.Path, got)
		}
	}
}

func TestHTTPProbesAddNoVariantForValueWithoutLettersOrDigits(t *testing.T) {
	// For these values the raw forms would repeat the plain escaping, so each value keeps its two shapes only.
	for _, v := range []string{"", "-", "+", " ", "/", "~", "½", "²"} {
		probes := httpPaths(t, "/orders/", []string{v})
		if len(probes) != 2 {
			t.Fatalf("value %q: want the plain and trailing-slash probes only, got %d: %+v", v, len(probes), probes)
		}
		for _, p := range probes {
			if strings.HasPrefix(p.Name, "probe-hex") {
				t.Fatalf("value %q gets a raw variant %q", v, p.Name)
			}
		}
	}
}

func TestHTTPProbePathsAreNotRepeated(t *testing.T) {
	values := []string{}
	distinct := map[string]bool{}
	for _, v := range append(domain.ProbeValues(1, 60), "1", "12", "a/b", "%", "é1", "٣1", "0") {
		if !distinct[v] {
			distinct[v] = true
			values = append(values, v)
		}
	}
	seen := map[string]string{}
	names := map[string]bool{}
	for _, p := range httpPaths(t, "/orders/", values) {
		if other, ok := seen[p.Path]; ok {
			t.Fatalf("path %q is probed twice (%s and %s)", p.Path, other, p.Name)
		}
		if names[p.Name] {
			t.Fatalf("case name %q is used twice", p.Name)
		}
		seen[p.Path] = p.Name
		names[p.Name] = true
	}
}

func TestProbeRequestCountIsBounded(t *testing.T) {
	values := domain.ProbeValues(2, 40)
	n := len(httpPaths(t, "/orders/", values))
	if n < 2*len(values) || n > 4*len(values) {
		t.Fatalf("HTTP probes for %d values: want between %d and %d, got %d", len(values), 2*len(values), 4*len(values), n)
	}
	t.Logf("HTTP probes for %d values (seed 2): %d", len(values), n)

	// The command-line probes are unchanged: two per value.
	var cli int
	for _, c := range ProbeCases("cli", "orders", "", values) {
		if c.Kind == "cli" {
			cli++
		}
	}
	if cli != 2*len(values) {
		t.Fatalf("CLI probes for %d values: want %d, got %d", len(values), 2*len(values), cli)
	}
}
