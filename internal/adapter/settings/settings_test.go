package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/usecase"
)

// writeConfig puts body at <root>/.go-anything/config.json and returns the root.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func mustLoad(t *testing.T, body string) usecase.PolicySettings {
	t.Helper()
	s, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("a valid file must load, got: %v", err)
	}
	return s
}

func TestPathIsDotDirUnderRoot(t *testing.T) {
	want := filepath.Join("proj", ".go-anything", "config.json")
	if got := Path("proj"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestMissingFileIsDefault(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a missing file is not an error, got: %v", err)
	}
	if p, src := usecase.ResolveDefectPolicy("orders", nil, s); p != usecase.PolicyDefect || src != usecase.SourceDefault {
		t.Fatalf("a missing file must give the default, got (%q, %q)", p, src)
	}
}

func TestInvalidValueNamesFileKeyAndValue(t *testing.T) {
	root := writeConfig(t, `{"defect_policy":"crash"}`)
	_, err := Load(root)
	if err == nil {
		t.Fatal("an invalid defect_policy must be an error")
	}
	msg := err.Error()
	for _, want := range []string{Path(root), `"defect_policy"`, `"crash"`, `"defect"`, `"expected"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the error must contain %q, got: %s", want, msg)
		}
	}
}

func TestInvalidUnitValueNamesUnitKey(t *testing.T) {
	root := writeConfig(t, `{"unit_defect_policy":{"orders":"Expected"}}`)
	_, err := Load(root)
	if err == nil {
		t.Fatal("an invalid unit value must be an error")
	}
	msg := err.Error()
	for _, want := range []string{Path(root), "unit_defect_policy.orders", `"Expected"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the error must contain %q, got: %s", want, msg)
		}
	}
}

func TestEmptyValueInFileIsRejected(t *testing.T) {
	for _, body := range []string{`{"defect_policy":""}`, `{"unit_defect_policy":{"orders":""}}`} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("an empty policy value must be rejected: %s", body)
		}
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	root := writeConfig(t, `{"defect_polcy":"expected"}`)
	_, err := Load(root)
	if err == nil {
		t.Fatal("a misspelt key must be rejected, not ignored")
	}
	if !strings.Contains(err.Error(), `"defect_polcy"`) || !strings.Contains(err.Error(), Path(root)) {
		t.Fatalf("the error must name the key and the file, got: %v", err)
	}
}

func TestNotAnObjectIsRejected(t *testing.T) {
	if _, err := Load(writeConfig(t, `["expected"]`)); err == nil {
		t.Fatal("a file that is not a JSON object must be rejected")
	}
}

func TestUnitEntryForOtherUnitIsIgnored(t *testing.T) {
	s := mustLoad(t, `{"unit_defect_policy":{"users":"expected"}}`)
	if p, src := usecase.ResolveDefectPolicy("orders", nil, s); p != usecase.PolicyDefect || src != usecase.SourceDefault {
		t.Fatalf("users' entry must not reach orders, got (%q, %q)", p, src)
	}
	if p, src := usecase.ResolveDefectPolicy("users", nil, s); p != usecase.PolicyExpected || src != usecase.SourceUnit {
		t.Fatalf("users must get its own entry, got (%q, %q)", p, src)
	}
}

func TestFileSourcesResolvePerUnit(t *testing.T) {
	s := mustLoad(t, `{"defect_policy":"expected","unit_defect_policy":{"orders":"defect"}}`)
	if p, src := usecase.ResolveDefectPolicy("orders", nil, s); p != usecase.PolicyDefect || src != usecase.SourceUnit {
		t.Fatalf("orders' own entry must win over the file default, got (%q, %q)", p, src)
	}
	if p, src := usecase.ResolveDefectPolicy("users", nil, s); p != usecase.PolicyExpected || src != usecase.SourceFile {
		t.Fatalf("users must take the file default, got (%q, %q)", p, src)
	}
}

func TestTrailingGarbageIsRejected(t *testing.T) {
	if _, err := Load(writeConfig(t, `{"defect_policy":"defect"} x`)); err == nil {
		t.Fatal("data after the JSON object must be rejected")
	}
}
