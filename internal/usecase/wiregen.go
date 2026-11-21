package usecase

import (
	"bytes"
	"fmt"
	"go/format"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// RegistryMarker is the first line of the registry file. wire gen rewrites the registry only when
// this marker is present, so a hand-written units.go is never overwritten.
const RegistryMarker = "// Registry written by go-anything wire gen. Regenerate it with `go-anything wire gen`; do not hand-edit."

var (
	unitNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	prefixPattern   = regexp.MustCompile(`^/[A-Za-z0-9_\-./]*/$`)
)

// ValidUnitName accepts a name that is also a valid Go package name. "units" is reserved for the registry.
func ValidUnitName(name string) error {
	if !unitNamePattern.MatchString(name) {
		return fmt.Errorf("wire: unit name %q must be lowercase letters, digits or _, starting with a letter", name)
	}
	if name == "units" {
		return fmt.Errorf("wire: unit name %q is reserved for the registry", name)
	}
	return nil
}

// ValidPrefix requires a URL path prefix that ends in "/" so it covers everything beneath it.
func ValidPrefix(prefix string) error {
	if !prefixPattern.MatchString(prefix) || strings.Contains(prefix, "..") {
		return fmt.Errorf("wire: prefix %q must look like /orders/ (starts and ends with /, no ..)", prefix)
	}
	return nil
}

var unitTemplates = map[string]string{
	"unit.go": `package {{.Unit}}

// Kind is how this unit is reached.
const Kind = "http"

// Prefix is the URL path this unit answers, as set by go-anything wire gen.
const Prefix = "{{.Prefix}}"
`,
	"rewrite.go": `package {{.Unit}}

import "net/http"

// Handle answers this unit with the rewrite. It stays a placeholder until the rewrite is written:
// the gate refuses placeholders, so an unfinished rewrite cannot pass as done.
func Handle(w http.ResponseWriter, r *http.Request) {
	panic("not implemented")
}
`,
	"original.go": `package {{.Unit}}

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Original forwards this unit's requests to the original service at base.
func Original(base *url.URL) http.Handler {
	return httputil.NewSingleHostReverseProxy(base)
}
`,
}

// UnitFiles returns the generated files for one unit, keyed by file name and already formatted.
func UnitFiles(unit, prefix string) (map[string][]byte, error) {
	if err := ValidUnitName(unit); err != nil {
		return nil, err
	}
	if err := ValidPrefix(prefix); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for name, src := range unitTemplates {
		t, err := template.New(name).Parse(src)
		if err != nil {
			return nil, fmt.Errorf("wire: template %s: %w", name, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, struct{ Unit, Prefix string }{unit, prefix}); err != nil {
			return nil, fmt.Errorf("wire: render %s: %w", name, err)
		}
		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			return nil, fmt.Errorf("wire: %s does not format: %w", name, err)
		}
		out[name] = formatted
	}
	return out, nil
}

// cliTemplates are the files for a command-line unit. The command is the unit's name: the first
// argument selects it. There is no original.go, because the original is run as a program.
var cliTemplates = map[string]string{
	"unit.go": `package {{.Unit}}

// Kind is how this unit is reached: as a command-line subcommand named after the unit.
const Kind = "cli"
`,
	"rewrite.go": `package {{.Unit}}

import "io"

// Handle runs this command with the rewrite. args is the whole argument list, command word first,
// exactly as the original receives it. It returns the exit code. It stays a placeholder until the
// rewrite is written: the gate refuses placeholders, so an unfinished rewrite cannot pass as done.
func Handle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	panic("not implemented")
}
`,
}

// CLIUnitFiles returns the generated files for one command-line unit.
func CLIUnitFiles(unit string) (map[string][]byte, error) {
	if err := ValidUnitName(unit); err != nil {
		return nil, err
	}
	return renderTemplates(cliTemplates, struct{ Unit, Prefix string }{unit, ""})
}

func renderTemplates(tpls map[string]string, data any) (map[string][]byte, error) {
	out := map[string][]byte{}
	for name, src := range tpls {
		t, err := template.New(name).Parse(src)
		if err != nil {
			return nil, fmt.Errorf("wire: template %s: %w", name, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("wire: render %s: %w", name, err)
		}
		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			return nil, fmt.Errorf("wire: %s does not format: %w", name, err)
		}
		out[name] = formatted
	}
	return out, nil
}

// UnitRef names a wired unit and how it is reached.
type UnitRef struct {
	Name string
	Kind string // "http" or "cli"
}

// Registry returns units.go wiring every listed unit: Mount for HTTP units and RunCLI for
// command-line units. module is the Go module path.
func Registry(module string, units []UnitRef) ([]byte, error) {
	sorted := append([]UnitRef(nil), units...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	data := struct {
		Marker, Module string
		Units          []UnitRef
	}{RegistryMarker, module, sorted}
	t, err := template.New("registry").Parse(registryTemplate)
	if err != nil {
		return nil, fmt.Errorf("wire: registry template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("wire: render registry: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("wire: registry does not format: %w", err)
	}
	return formatted, nil
}

const registryTemplate = `{{.Marker}}

// Package units wires every unit of this program. HTTP units are mounted on a mux; command-line units
// are reached through RunCLI. A unit serves the original unless GA_UNIT_<NAME>=rewrite is set.
package units

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
{{range .Units}}
	"{{$.Module}}/units/{{.Name}}"{{end}}
)

// Mount wires every HTTP unit. originalBase is the URL of the original service (for example from
// GA_ORIGINAL_URL). A unit with no GA_UNIT_<NAME> set serves the original, so nothing unverified
// runs by default.
func Mount(mux *http.ServeMux, originalBase string) error {
	base, err := url.Parse(originalBase)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("units: original base %q must be an absolute URL", originalBase)
	}
	_ = base
{{- range .Units}}{{if eq .Kind "http"}}
	if err := choose(mux, "{{.Name}}", {{.Name}}.Prefix, {{.Name}}.Handle, {{.Name}}.Original(base)); err != nil {
		return err
	}{{end}}{{end}}
	return nil
}

func choose(mux *http.ServeMux, unit, prefix string, rewrite http.HandlerFunc, original http.Handler) error {
	env := "GA_UNIT_" + strings.ToUpper(unit)
	switch os.Getenv(env) {
	case "", "original":
		mux.Handle(prefix, original)
	case "rewrite":
		mux.HandleFunc(prefix, rewrite)
	case "shadow":
		mux.Handle(prefix, shadowHTTP(unit, rewrite, original))
	default:
		return fmt.Errorf("units: %s must be rewrite, original or shadow", env)
	}
	return nil
}

// shadowHTTP answers each request with the original, and runs the rewrite on a copy of the same request.
// Differences are logged, never shown to the caller: a shadow run changes nothing for users.
func shadowHTTP(unit string, rewrite http.HandlerFunc, original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "units: read request body", http.StatusBadRequest)
			return
		}
		r.Body.Close()
		origRec := httptest.NewRecorder()
		original.ServeHTTP(origRec, withBody(r, body))
		rewRec := httptest.NewRecorder()
		safeHTTP(rewrite, rewRec, withBody(r, body))
		if origRec.Code != rewRec.Code || origRec.Body.String() != rewRec.Body.String() {
			log.Printf("units: shadow %s differs on %s %s: original %d %q, rewrite %d %q",
				unit, r.Method, r.URL.Path, origRec.Code, origRec.Body.String(), rewRec.Code, rewRec.Body.String())
		}
		for k, vs := range origRec.Header() {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(origRec.Code)
		w.Write(origRec.Body.Bytes())
	})
}

func withBody(r *http.Request, body []byte) *http.Request {
	c := r.Clone(r.Context())
	c.Body = io.NopCloser(bytes.NewReader(body))
	return c
}

// safeHTTP runs the rewrite and turns a panic (a placeholder, a bug) into a 500 on that recorder only.
func safeHTTP(rewrite http.HandlerFunc, w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			http.Error(w, fmt.Sprint("rewrite panicked: ", p), http.StatusInternalServerError)
		}
	}()
	rewrite(w, r)
}

// RunCLI handles the command if its first argument names a command-line unit. It returns the exit code
// and true when it handled the command, and false when the program should handle it itself.
// originalBin is the original program (for example from GA_ORIGINAL_CLI); it runs with the same
// arguments, input and output.
func RunCLI(originalBin string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
{{- range .Units}}{{if eq .Kind "cli"}}
	case "{{.Name}}":
		return cliChoose("{{.Name}}", originalBin, {{.Name}}.Handle, args, stdin, stdout, stderr), true{{end}}{{end}}
	}
	return 0, false
}

func cliChoose(unit, originalBin string, rewrite func(args []string, stdin io.Reader, stdout, stderr io.Writer) int, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	env := "GA_UNIT_" + strings.ToUpper(unit)
	switch os.Getenv(env) {
	case "", "original":
		if originalBin == "" {
			fmt.Fprintf(stderr, "units: %s serves the original, so GA_ORIGINAL_CLI must name the original program\n", unit)
			return 2
		}
		return runOriginal(originalBin, args, stdin, stdout, stderr)
	case "rewrite":
		return rewrite(args, stdin, stdout, stderr)
	case "shadow":
		return cliShadow(unit, originalBin, rewrite, args, stdin, stdout, stderr)
	}
	fmt.Fprintf(stderr, "units: %s must be rewrite, original or shadow\n", env)
	return 2
}

// cliShadow runs the original, which owns the real output and exit code, and runs the rewrite on the
// same arguments and input into buffers. A difference is logged; the caller never sees the rewrite.
func cliShadow(unit, originalBin string, rewrite func(args []string, stdin io.Reader, stdout, stderr io.Writer) int, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if originalBin == "" {
		fmt.Fprintf(stderr, "units: %s shadows the original, so GA_ORIGINAL_CLI must name the original program\n", unit)
		return 2
	}
	input, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "units: %s: read input: %v\n", unit, err)
		return 2
	}
	var oOut, oErr bytes.Buffer
	oCode := runOriginal(originalBin, args, bytes.NewReader(input), &oOut, &oErr)
	var rOut, rErr bytes.Buffer
	rCode := safeCLI(rewrite, args, bytes.NewReader(input), &rOut, &rErr)
	stdout.Write(oOut.Bytes())
	stderr.Write(oErr.Bytes())
	if oCode != rCode || oOut.String() != rOut.String() || oErr.String() != rErr.String() {
		log.Printf("units: shadow %s differs on %q: original exit %d stdout %q, rewrite exit %d stdout %q",
			unit, args, oCode, oOut.String(), rCode, rOut.String())
	}
	return oCode
}

// safeCLI runs the rewrite; a panic becomes exit 2 with the panic text on its own stderr buffer.
func safeCLI(rewrite func(args []string, stdin io.Reader, stdout, stderr io.Writer) int, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintf(stderr, "rewrite panicked: %v\n", p)
			code = 2
		}
	}()
	return rewrite(args, stdin, stdout, stderr)
}

func runOriginal(bin string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	fmt.Fprintf(stderr, "units: run original %s: %v\n", bin, err)
	return 2
}
`
