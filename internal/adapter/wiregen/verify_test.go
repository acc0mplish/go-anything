package wiregen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListenAddressReadsLiteral(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main

import "net/http"

func main() {
	http.ListenAndServe("127.0.0.1:19999", nil)
}
`), 0o644)
	addr, err := ListenAddress(root, "main.go")
	if err != nil || addr != "127.0.0.1:19999" {
		t.Fatalf("want 127.0.0.1:19999, got %q err=%v", addr, err)
	}
}

func TestListenAddressRefusesNonLiteral(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main

import ("net/http"; "os")

func main() {
	http.ListenAndServe(os.Getenv("ADDR"), nil)
}
`), 0o644)
	if _, err := ListenAddress(root, "main.go"); err == nil {
		t.Fatal("a non-literal address must be refused, with a pointer to the manual path")
	}
}
