package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func headerServer(t *testing.T, contentType, extra string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		if extra != "" {
			w.Header().Set("X-Feature", extra)
		}
		w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDefaultCapturesContentTypeOnly(t *testing.T) {
	srv := headerServer(t, "application/json", "on")
	got, err := (Runner{BaseURL: srv.URL}).Do(domain.AppCase{Name: "h", Kind: "http", Method: "GET", Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["Content-Type"] != "application/json" {
		t.Fatalf("default must capture Content-Type: %+v", got.Headers)
	}
	if _, ok := got.Headers["X-Feature"]; ok {
		t.Fatal("headers outside the default set must not be captured")
	}
}

func TestCaseCanNameItsOwnHeaders(t *testing.T) {
	srv := headerServer(t, "text/plain", "on")
	c := domain.AppCase{Name: "h", Kind: "http", Method: "GET", Path: "/",
		Headers: map[string]string{"x-feature": ""}}
	got, err := (Runner{BaseURL: srv.URL}).Do(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["X-Feature"] != "on" {
		t.Fatalf("a named header must be captured under its canonical name: %+v", got.Headers)
	}
}

func TestMissingHeaderIsNotInventedAsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Del("Content-Type")
		w.WriteHeader(204)
	}))
	t.Cleanup(srv.Close)
	got, _ := (Runner{BaseURL: srv.URL}).Do(domain.AppCase{Name: "h", Kind: "http", Method: "GET", Path: "/"})
	if _, ok := got.Headers["Content-Type"]; ok {
		t.Fatalf("an absent header must not be recorded: %+v", got.Headers)
	}
}
