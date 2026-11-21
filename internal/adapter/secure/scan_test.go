package secure

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

// fixtureModule is the module path of the test fixture.
const fixtureModule = "example.com/securefixture"

// cleanSource is a file without violations: it keeps SF-5.1 passing and everything else calm.
const cleanSource = `package fixture

import "log"

// Ready reports that the module logs its startup.
func Ready() {
	log.Println("service ready")
}
`

// quietSource has no logging at all, for the SF-5.1 failure case.
const quietSource = `package fixture

// Add is intentionally silent.
func Add(a, b int) int {
	return a + b
}
`

// vetFailSource compiles but triggers a go vet printf finding.
const vetFailSource = `package fixture

import "fmt"

// Show plants a vet finding.
func Show() {
	fmt.Printf("%d", "not a number")
}
`

// fixture describes one throwaway module written into a temp dir.
type fixture struct {
	files     map[string]string // extra files, relative path -> content
	omitClean bool              // do not write the clean file
	noGoSum   bool              // do not write go.sum
}

// writeModule writes the fixture and returns its root path.
func writeModule(t *testing.T, fx fixture) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module "+fixtureModule+"\n\ngo 1.24\n")
	if !fx.noGoSum {
		write("go.sum", "")
	}
	if !fx.omitClean {
		write("clean.go", cleanSource)
	}
	for name, content := range fx.files {
		write(name, content)
	}
	return root
}

// resultByItem returns the result for one checklist item.
func resultByItem(t *testing.T, rep domain.SecureReport, id string) domain.SecureResult {
	t.Helper()
	for _, res := range rep.Results {
		if res.Item == id {
			return res
		}
	}
	t.Fatalf("report has no result for %s", id)
	return domain.SecureResult{}
}

// fakeTools points the tool binaries at fakes: a missing govulncheck name (skip) and /bin/true
// for go vet (instant pass). The returned restore function puts the real values back.
func fakeTools(t *testing.T) func() {
	t.Helper()
	oldGv, oldGo := govulncheckBin, goBin
	govulncheckBin = "secure-test-missing-govulncheck"
	if _, err := exec.LookPath("/bin/true"); err == nil {
		goBin = "/bin/true"
	} else {
		goBin = "secure-test-missing-go"
	}
	return func() { govulncheckBin, goBin = oldGv, oldGo }
}

func TestRunAutoChecks(t *testing.T) {
	restore := fakeTools(t)
	defer restore()

	cases := []struct {
		name        string
		item        string
		want        domain.SecureStatus
		detailHas   []string
		detailLacks string
		noteHas     string
		file        string
		files       map[string]string
		omitClean   bool
		noGoSum     bool
	}{
		{
			name:        "aws access key literal fails SF-1.1",
			item:        "SF-1.1",
			want:        domain.SecureFail,
			detailHas:   []string{"AWS access key id"},
			detailLacks: "AKIA1234567890ABCDEF",
			file: `package fixture

// AccessKey plants a credential shape for the test.
const AccessKey = "AKIA1234567890ABCDEF"
`,
		},
		{
			name:        "password assignment fails SF-1.1",
			item:        "SF-1.1",
			want:        domain.SecureFail,
			detailHas:   []string{"password"},
			detailLacks: "hunter2hunter2",
			file: `package fixture

// Connect plants a hardcoded password assignment.
func Connect() {
	password := "hunter2hunter2"
	_ = password
}
`,
		},
		{
			name:      "pprof import fails SF-2.1",
			item:      "SF-2.1",
			want:      domain.SecureFail,
			detailHas: []string{"net/http/pprof"},
			noteHas:   "non-test files only",
			file: `package fixture

import _ "net/http/pprof"
`,
		},
		{
			name:      "md5 import fails SF-3.1",
			item:      "SF-3.1",
			want:      domain.SecureFail,
			detailHas: []string{"crypto/md5"},
			file: `package fixture

import _ "crypto/md5"
`,
		},
		{
			name:      "rc4 import fails SF-3.2",
			item:      "SF-3.2",
			want:      domain.SecureFail,
			detailHas: []string{"crypto/rc4"},
			file: `package fixture

import _ "crypto/rc4"
`,
		},
		{
			name:      "math rand import fails SF-3.3",
			item:      "SF-3.3",
			want:      domain.SecureFail,
			detailHas: []string{"math/rand"},
			file: `package fixture

import _ "math/rand"
`,
		},
		{
			name:      "InsecureSkipVerify true fails SF-3.4",
			item:      "SF-3.4",
			want:      domain.SecureFail,
			detailHas: []string{"InsecureSkipVerify"},
			file: `package fixture

import "crypto/tls"

// DialConfig plants a TLS verification bypass.
func DialConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true}
}
`,
		},
		{
			name: "InsecureSkipVerify false passes SF-3.4",
			item: "SF-3.4",
			want: domain.SecurePass,
			file: `package fixture

import "crypto/tls"

// StrictConfig keeps certificate verification on.
func StrictConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: false}
}
`,
		},
		{
			name:      "remote http URL fails SF-3.5",
			item:      "SF-3.5",
			want:      domain.SecureFail,
			detailHas: []string{"http"},
			file: `package fixture

// Endpoint plants a plaintext URL.
const Endpoint = "http://update.example.com/feed"
`,
		},
		{
			name: "loopback http URLs pass SF-3.5",
			item: "SF-3.5",
			want: domain.SecurePass,
			file: `package fixture

// DebugURL plants loopback URLs, a test and development pattern.
const (
	DebugURL = "http://localhost:9090/debug"
	HomeURL  = "http://127.0.0.1:8080/home"
)
`,
		},
		{
			name:      "concatenated SQL fails SF-4.1",
			item:      "SF-4.1",
			want:      domain.SecureFail,
			detailHas: []string{"Query"},
			file: `package fixture

import "database/sql"

// Find plants SQL assembly by concatenation.
func Find(db *sql.DB, id string) (*sql.Rows, error) {
	return db.Query("SELECT * FROM users WHERE id = " + id)
}
`,
		},
		{
			name:      "Sprintf SQL into Exec fails SF-4.1",
			item:      "SF-4.1",
			want:      domain.SecureFail,
			detailHas: []string{"Sprintf"},
			file: `package fixture

import (
	"database/sql"
	"fmt"
)

// Remove plants SQL assembly via Sprintf.
func Remove(db *sql.DB, id string) error {
	_, err := db.Exec(fmt.Sprintf("DELETE FROM users WHERE id = %s", id))
	return err
}
`,
		},
		{
			name:      "bare Sprintf with SQL keywords fails SF-4.1",
			item:      "SF-4.1",
			want:      domain.SecureFail,
			detailHas: []string{"Sprintf"},
			file: `package fixture

import "fmt"

// Build plants a SQL-shaped Sprintf outside a direct query call.
func Build(id string) string {
	return fmt.Sprintf("UPDATE users SET name = %s WHERE id = %s", id, id)
}
`,
		},
		{
			name:      "assembled command fails SF-4.2",
			item:      "SF-4.2",
			want:      domain.SecureFail,
			detailHas: []string{"not a string literal"},
			file: `package fixture

import (
	"context"
	"os/exec"
)

// RunCommand plants an assembled command name.
func RunCommand(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, name, "--version").Run()
}
`,
		},
		{
			name:      "silent module fails SF-5.1",
			item:      "SF-5.1",
			want:      domain.SecureFail,
			detailHas: []string{"no logging calls found in non-test sources"},
			file:      quietSource,
			omitClean: true,
		},
		{
			name:        "password in a log line fails SF-5.2",
			item:        "SF-5.2",
			want:        domain.SecureFail,
			detailHas:   []string{"password"},
			detailLacks: "hunter2hunter2",
			file: `package fixture

import "log"

// Audit plants a secret marker in a log line.
func Audit(pw string) {
	log.Printf("login failed for user, password=%s", pw)
}
`,
		},
		{
			name:      "cookie without flags fails SF-6.1",
			item:      "SF-6.1",
			want:      domain.SecureFail,
			detailHas: []string{"Secure", "HttpOnly"},
			file: `package fixture

import "net/http"

// Issue plants a cookie without security attributes.
func Issue(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{Name: "session", Value: value, Path: "/"})
}
`,
		},
		{
			name: "cookie with both flags passes SF-6.1",
			item: "SF-6.1",
			want: domain.SecurePass,
			file: `package fixture

import "net/http"

// IssueSecure sets both cookie flags.
func IssueSecure(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{Name: "session", Value: value, Path: "/", Secure: true, HttpOnly: true})
}
`,
		},
		{
			name: "missing go.sum fails SF-7.2 when a require exists",
			item: "SF-7.2",
			want: domain.SecureFail,
			// A dependency-free module has no go.sum by design; only a require makes its absence a
			// pinning failure, so this case carries its own go.mod with one.
			files: map[string]string{"go.mod": "module " + fixtureModule + "\n\ngo 1.24\n\nrequire golang.org/x/mod v0.42.0\n"},
			file: `package fixture

// Pinned is not a violation.
const Pinned = true
`,
			noGoSum: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]string{"planted.go": tc.file}
			for name, content := range tc.files {
				extra[name] = content
			}
			root := writeModule(t, fixture{
				files:     extra,
				omitClean: tc.omitClean,
				noGoSum:   tc.noGoSum,
			})
			rep, err := Run(root)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			res := resultByItem(t, rep, tc.item)
			if res.Status != tc.want {
				t.Fatalf("%s status = %q, want %q, findings %+v", tc.item, res.Status, tc.want, res.Findings)
			}
			if tc.noteHas != "" && !strings.Contains(res.Note, tc.noteHas) {
				t.Fatalf("note %q does not contain %q", res.Note, tc.noteHas)
			}
			if tc.want == domain.SecurePass {
				if len(res.Findings) != 0 {
					t.Fatalf("%s passed with findings %+v", tc.item, res.Findings)
				}
				return
			}
			if len(res.Findings) == 0 {
				t.Fatalf("%s failed without findings", tc.item)
			}
			first := res.Findings[0]
			if first.Pos == "" {
				t.Fatalf("%s finding has no position: %+v", tc.item, first)
			}
			for _, want := range tc.detailHas {
				if !strings.Contains(first.Detail, want) {
					t.Fatalf("detail %q does not contain %q", first.Detail, want)
				}
			}
			if tc.detailLacks != "" && strings.Contains(first.Detail, tc.detailLacks) {
				t.Fatalf("detail %q leaks the secret %q", first.Detail, tc.detailLacks)
			}
		})
	}
}

func TestRunCleanModule(t *testing.T) {
	restore := fakeTools(t)
	defer restore()

	// Violations hidden in places the walk must skip: test files, vendor, testdata and a
	// hidden directory.
	root := writeModule(t, fixture{files: map[string]string{
		"skipme_test.go":              "package fixture\n\nconst TestKey = \"AKIA1234567890ABCDEF\"\n",
		"vendor/vendored/vendored.go": "package vendored\n\nconst Key = \"AKIA1234567890ABCDEF\"\n",
		"testdata/td.go":              "package td\n\nconst Key = \"AKIA1234567890ABCDEF\"\n",
		".hidden/h.go":                "package hidden\n\nconst Key = \"AKIA1234567890ABCDEF\"\n",
	}})
	rep, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Root != filepath.Clean(root) {
		t.Fatalf("Root = %q, want %q", rep.Root, root)
	}
	if rep.ModulePath != fixtureModule {
		t.Fatalf("ModulePath = %q, want %q", rep.ModulePath, fixtureModule)
	}
	if rep.GoVersion != "1.24" {
		t.Fatalf("GoVersion = %q, want 1.24", rep.GoVersion)
	}
	if rep.ScannedFiles != 1 {
		t.Fatalf("ScannedFiles = %d, want 1 (only clean.go)", rep.ScannedFiles)
	}
	if rep.ToolVersion != Version {
		t.Fatalf("ToolVersion = %q, want %q", rep.ToolVersion, Version)
	}
	if rep.GeneratedAt.IsZero() {
		t.Fatal("GeneratedAt is zero")
	}
	if len(rep.Results) != len(rep.Items) {
		t.Fatalf("%d results for %d items", len(rep.Results), len(rep.Items))
	}
	for i, res := range rep.Results {
		if res.Item != rep.Items[i].ID {
			t.Fatalf("result %d is %s, want %s (results must follow item order)", i, res.Item, rep.Items[i].ID)
		}
	}
	want := map[string]domain.SecureStatus{}
	for _, it := range domain.SecurityChecklist() {
		if it.Method == domain.SecureManual {
			want[it.ID] = domain.SecureStatusManual
		} else {
			want[it.ID] = domain.SecurePass
		}
	}
	want["SF-7.1"] = domain.SecureSkip // govulncheck is faked as missing
	if goBin == "secure-test-missing-go" {
		want["SF-10.1"] = domain.SecureSkip // /bin/true unavailable on this system
	}
	for id, status := range want {
		if got := resultByItem(t, rep, id).Status; got != status {
			t.Fatalf("%s status = %q, want %q", id, got, status)
		}
	}
	if res := resultByItem(t, rep, "SF-7.1"); !strings.Contains(res.Note, "not found in PATH") {
		t.Fatalf("SF-7.1 note = %q, want it to mention PATH", res.Note)
	}
}

func TestRunGovulncheckTool(t *testing.T) {
	old := govulncheckBin
	defer func() { govulncheckBin = old }()
	root := writeModule(t, fixture{})

	govulncheckBin = "secure-test-missing-govulncheck"
	rep, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := resultByItem(t, rep, "SF-7.1")
	if res.Status != domain.SecureSkip || !strings.Contains(res.Note, "not found in PATH") || len(res.Findings) != 0 {
		t.Fatalf("missing govulncheck: status %q note %q findings %+v", res.Status, res.Note, res.Findings)
	}

	if _, err := exec.LookPath("/bin/true"); err != nil {
		t.Skip("/bin/true is not available on this system")
	}
	govulncheckBin = "/bin/true"
	rep, err = Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res := resultByItem(t, rep, "SF-7.1"); res.Status != domain.SecurePass {
		t.Fatalf("exit 0: status %q, want pass, note %q", res.Status, res.Note)
	}

	if _, err := exec.LookPath("/bin/false"); err != nil {
		t.Skip("/bin/false is not available on this system")
	}
	govulncheckBin = "/bin/false"
	rep, err = Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res = resultByItem(t, rep, "SF-7.1")
	if res.Status != domain.SecureFail || len(res.Findings) == 0 {
		t.Fatalf("nonzero exit: status %q findings %+v, want fail with findings", res.Status, res.Findings)
	}
}

func TestRunVetToolPass(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not in PATH")
	}
	old := govulncheckBin
	govulncheckBin = "secure-test-missing-govulncheck"
	defer func() { govulncheckBin = old }()

	root := writeModule(t, fixture{})
	rep, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := resultByItem(t, rep, "SF-10.1")
	if res.Status != domain.SecurePass {
		t.Fatalf("status %q, want pass, note %q findings %+v", res.Status, res.Note, res.Findings)
	}
}

func TestRunVetToolFindings(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not in PATH")
	}
	old := govulncheckBin
	govulncheckBin = "secure-test-missing-govulncheck"
	defer func() { govulncheckBin = old }()

	root := writeModule(t, fixture{files: map[string]string{"vetfail.go": vetFailSource}})
	rep, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := resultByItem(t, rep, "SF-10.1")
	if res.Status != domain.SecureFail || len(res.Findings) == 0 {
		t.Fatalf("status %q findings %+v, want fail with findings", res.Status, res.Findings)
	}
	if !strings.Contains(res.Findings[0].Detail, "Printf") {
		t.Fatalf("detail %q does not mention Printf", res.Findings[0].Detail)
	}
	if !strings.Contains(res.Findings[0].Pos, "vetfail.go") {
		t.Fatalf("pos %q does not mention vetfail.go", res.Findings[0].Pos)
	}
}

func TestRunVetToolStartupSkip(t *testing.T) {
	old := goBin
	goBin = "secure-test-missing-go"
	defer func() { goBin = old }()

	root := writeModule(t, fixture{})
	rep, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := resultByItem(t, rep, "SF-10.1")
	if res.Status != domain.SecureSkip || !strings.Contains(res.Note, "not found in PATH") {
		t.Fatalf("status %q note %q, want skip with a PATH note", res.Status, res.Note)
	}
}

func TestRunRequiresGoMod(t *testing.T) {
	if _, err := Run(t.TempDir()); err == nil {
		t.Fatal("Run on a directory without go.mod must return an error")
	}
}

// TestGoSumNotNeededWithoutDependencies pins the dependency-free case: go does not write a go.sum
// for a module with no requires, so SF-7.2 must pass with a note instead of failing.
func TestGoSumNotNeededWithoutDependencies(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/depfree\n\ngo 1.27.4\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "m.go"), []byte("package depfree\n"), 0o600)
	rep, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Item == "SF-7.2" {
			if r.Status != domain.SecurePass {
				t.Fatalf("a dependency-free module has no go.sum and must pass: %+v", r)
			}
			if r.Note == "" {
				t.Fatal("the pass must carry the note explaining the missing go.sum")
			}
			return
		}
	}
	t.Fatal("SF-7.2 result missing")
}

// TestGoSumRequiredWithDependencies pins the failing half: a require without a go.sum fails.
func TestGoSumRequiredWithDependencies(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/dep\n\ngo 1.27.4\n\nrequire golang.org/x/mod v0.42.0\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "m.go"), []byte("package dep\n"), 0o600)
	rep, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Item == "SF-7.2" {
			if r.Status != domain.SecureFail {
				t.Fatalf("a module with a require and no go.sum must fail: %+v", r)
			}
			return
		}
	}
	t.Fatal("SF-7.2 result missing")
}
