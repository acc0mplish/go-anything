package usecase

import (
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestReplayFlagsChangedContentType(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "h", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: "ok",
			Headers: map[string]string{"Content-Type": "application/json"}},
	}}
	v, err := AppReplay(rec, fakeRunner{"h": {Status: 200, ResponseBody: "ok",
		Headers: map[string]string{"Content-Type": "text/html"}}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Passed() {
		t.Fatal("a changed Content-Type must fail even when status and body match")
	}
	if len(v.Mismatches) != 1 || v.Mismatches[0].What != "header Content-Type" {
		t.Fatalf("the header must be named: %+v", v.Mismatches)
	}
}

func TestReplayIgnoresUnrecordedHeaders(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "h", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: "ok"},
	}}
	v, _ := AppReplay(rec, fakeRunner{"h": {Status: 200, ResponseBody: "ok",
		Headers: map[string]string{"Date": "now", "Content-Length": "2"}}})
	if !v.Passed() {
		t.Fatalf("volatile headers that were never recorded must not fail: %+v", v.Mismatches)
	}
}

func TestReplayFlagsHeaderThatDisappeared(t *testing.T) {
	rec := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "h", Kind: "http", Method: "GET", Path: "/", Status: 200, ResponseBody: "ok",
			Headers: map[string]string{"Content-Type": "application/json"}},
	}}
	v, _ := AppReplay(rec, fakeRunner{"h": {Status: 200, ResponseBody: "ok"}})
	if v.Passed() {
		t.Fatal("a recorded header that is now missing must fail")
	}
}
