package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func contentTypeOf(t *testing.T, c domain.AppCase) string {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if _, err := (Runner{BaseURL: srv.URL}).Do(c); err != nil {
		t.Fatalf("do: %v", err)
	}
	return got
}

func TestJSONBodyWithoutDeclaredTypeIsSentAsJSON(t *testing.T) {
	got := contentTypeOf(t, domain.AppCase{Kind: "http", Method: "PUT", Path: "/x", Body: `{"display_name":"A"}`})
	if got != "application/json" {
		t.Fatalf("a JSON body needs its type or a JSON server answers 415; got %q", got)
	}
}

func TestDeclaredContentTypeWins(t *testing.T) {
	got := contentTypeOf(t, domain.AppCase{Kind: "http", Method: "POST", Path: "/x", Body: `{"a":1}`,
		RequestHeaders: map[string]string{"Content-Type": "application/vnd.custom+json"}})
	if got != "application/vnd.custom+json" {
		t.Fatalf("a declared type must not be overwritten; got %q", got)
	}
}

func TestNonJSONBodyGetsNoGuessedType(t *testing.T) {
	got := contentTypeOf(t, domain.AppCase{Kind: "http", Method: "POST", Path: "/x", Body: "a=1&b=2"})
	if got != "" {
		t.Fatalf("a form body must not be labeled JSON; got %q", got)
	}
}
