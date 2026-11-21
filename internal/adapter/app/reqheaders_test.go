package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func echoAuth(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	seen := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestRequestHeaderIsSentWithEnvExpanded(t *testing.T) {
	t.Setenv("GA_TEST_TOKEN", "s3cret")
	srv, seen := echoAuth(t)
	c := domain.AppCase{Name: "me", Kind: "http", Method: "GET", Path: "/me",
		RequestHeaders: map[string]string{"Authorization": "Bearer ${GA_TEST_TOKEN}"}}
	if _, err := (Runner{BaseURL: srv.URL}).Do(c); err != nil {
		t.Fatal(err)
	}
	if *seen != "Bearer s3cret" {
		t.Fatalf("server saw %q", *seen)
	}
}

func TestMissingEnvForHeaderIsErrorNotSilent(t *testing.T) {
	srv, _ := echoAuth(t)
	c := domain.AppCase{Name: "me", Kind: "http", Method: "GET", Path: "/me",
		RequestHeaders: map[string]string{"Authorization": "Bearer ${GA_TEST_UNSET_VAR_X}"}}
	_, err := (Runner{BaseURL: srv.URL}).Do(c)
	if err == nil || !strings.Contains(err.Error(), "GA_TEST_UNSET_VAR_X") {
		t.Fatalf("a missing credential must be an error naming the variable: %v", err)
	}
}

func TestRecordingStoresPlaceholderNotSecret(t *testing.T) {
	t.Setenv("GA_TEST_TOKEN2", "topsecretvalue")
	srv, _ := echoAuth(t)
	c := domain.AppCase{Name: "me", Kind: "http", Method: "GET", Path: "/me",
		RequestHeaders: map[string]string{"Authorization": "Bearer ${GA_TEST_TOKEN2}"}}
	// the case as written, which is what the recording keeps, must not contain the secret
	data, _ := json.Marshal(c)
	if strings.Contains(string(data), "topsecretvalue") {
		t.Fatal("the recorded case must hold the placeholder")
	}
	if _, err := (Runner{BaseURL: srv.URL}).Do(c); err != nil {
		t.Fatal(err)
	}
}
