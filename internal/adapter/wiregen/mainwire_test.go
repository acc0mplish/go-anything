package wiregen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const typicalMain = `package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	log.Fatal(http.ListenAndServe("127.0.0.1:0", mux))
}
`

const typicalCLI = `package main

import "fmt"

func main() {
	fmt.Println("tool")
}
`

func moduleWith(t *testing.T, mainSrc string) string {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.27.4\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSrc), 0o644)
	return root
}

func readMain(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWireMainAddsHTTPMountAndModuleStillBuilds(t *testing.T) {
	root := moduleWith(t, typicalMain)
	if _, err := Gen(root, "orders", "/orders/"); err != nil {
		t.Fatal(err)
	}
	changed, err := WireMain(root, "main.go", "example.com/app", "http")
	if err != nil || !changed {
		t.Fatalf("the mount must be added: changed=%v err=%v", changed, err)
	}
	s := readMain(t, root)
	for _, want := range []string{MountMarker, `units.Mount(mux, os.Getenv("GA_ORIGINAL_URL"))`, `"example.com/app/units"`, `"os"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("main.go missing %q:\n%s", want, s)
		}
	}
	if strings.Index(s, MountMarker) > strings.Index(s, "http.ListenAndServe") {
		t.Fatal("the mount must be placed before ListenAndServe")
	}
}

func TestWireMainAddsCLIRoutingAtStartOfMain(t *testing.T) {
	root := moduleWith(t, typicalCLI)
	if _, err := Gen(root, "orders", ""); err != nil {
		t.Fatal(err)
	}
	changed, err := WireMain(root, "main.go", "example.com/app", "cli")
	if err != nil || !changed {
		t.Fatalf("CLI routing must be added: changed=%v err=%v", changed, err)
	}
	s := readMain(t, root)
	if !strings.Contains(s, `units.RunCLI(os.Getenv("GA_ORIGINAL_CLI"), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)`) {
		t.Fatalf("RunCLI call missing:\n%s", s)
	}
	if strings.Index(s, CLIMarker) > strings.Index(s, `fmt.Println("tool")`) {
		t.Fatal("the routing must come before the program's own output")
	}
	if strings.Contains(s, `"log"`) {
		t.Fatal("a CLI program must not gain an unused log import")
	}
}

func TestWireMainIsIdempotentForBothKinds(t *testing.T) {
	root := moduleWith(t, typicalCLI)
	Gen(root, "orders", "")
	WireMain(root, "main.go", "example.com/app", "cli")
	first := readMain(t, root)
	changed, err := WireMain(root, "main.go", "example.com/app", "cli")
	if err != nil || changed {
		t.Fatalf("a second run must change nothing: changed=%v err=%v", changed, err)
	}
	if readMain(t, root) != first {
		t.Fatal("main.go must be byte-identical after a second run")
	}
}

func TestWireMainWithNilHandlerUsesDefaultMux(t *testing.T) {
	root := moduleWith(t, `package main

import (
	"log"
	"net/http"
)

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:0", nil))
}
`)
	Gen(root, "orders", "/orders/")
	if _, err := WireMain(root, "main.go", "example.com/app", "http"); err != nil {
		t.Fatalf("nil handler must wire to the default mux: %v", err)
	}
	if !strings.Contains(readMain(t, root), "units.Mount(http.DefaultServeMux") {
		t.Fatal("default mux not used")
	}
}

func TestWireMainRestoresFileWhenBuildFails(t *testing.T) {
	broken := `package main

import (
	"log"
	"net/http"
)

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:0", mux))
	mux := http.NewServeMux()
	_ = mux
}
`
	root := moduleWith(t, broken)
	Gen(root, "orders", "/orders/")
	if _, err := WireMain(root, "main.go", "example.com/app", "http"); err == nil {
		t.Fatal("a build failure must be reported")
	}
	if readMain(t, root) != broken {
		t.Fatal("after a failed build the original main.go must be restored byte for byte")
	}
}

func TestWireMainRefusesExpressionHandler(t *testing.T) {
	root := moduleWith(t, `package main

import (
	"log"
	"net/http"
)

func build() http.Handler { return http.NewServeMux() }

func main() {
	log.Fatal(http.ListenAndServe("127.0.0.1:0", build()))
}
`)
	Gen(root, "orders", "/orders/")
	if _, err := WireMain(root, "main.go", "example.com/app", "http"); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("an expression handler must be refused with instructions: %v", err)
	}
}
