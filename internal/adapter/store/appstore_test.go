package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func TestAppSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.json")
	in := domain.AppRecording{Subject: "svc", Cases: []domain.AppCase{
		{Name: "h", Kind: "http", Method: "GET", Path: "/x", Status: 200, ResponseBody: "ok"},
	}}
	if err := SaveApp(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadApp(p)
	if err != nil || out.Subject != "svc" || out.Cases[0].ResponseBody != "ok" {
		t.Fatalf("round trip failed: %+v err=%v", out, err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("recording must be private, mode=%v", fi.Mode().Perm())
	}
}

func TestAppSaveRefusesInvalid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.json")
	if err := SaveApp(p, domain.AppRecording{}); err == nil {
		t.Fatal("empty app recording must not be saved")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("nothing may be written for an invalid recording")
	}
}
