package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// The registry in commands.go must agree with the dispatch in main.go.
//
// This test reads main.go's syntax tree instead of calling run(). Calling run() with each command
// is not safe: `mcp` reads stdin and serves, `gate` runs go build and go test, `frontdoor` listens.
// Parsing runs nothing, so no build, server or agent is started.
//
// It checks, for every toolRegistry CLI entry:
//   - the first word is a case label of the switch on args[0] in func run();
//   - for a two-word entry ("app record", "wire gen"), the case in run() calls the second-level
//     dispatcher (see subcommandDispatchers), and that dispatcher's switch has the second word as a case.
//
// It checks one direction only: a command that run() dispatches but the registry lacks is not reported.

// subcommandDispatchers maps a first word that has second-level words to the function that switches on them.
var subcommandDispatchers = map[string]string{
	"app":       "cmdApp",
	"wire":      "cmdWire",
	"shadow":    "cmdShadowEntry",
	"frontdoor": "cmdFrontDoorEntry",
}

// dispatchSwitch is the switch on args[0] found in one function.
type dispatchSwitch struct {
	pos   token.Pos                  // where the switch starts, for error messages
	cases map[string]*ast.CaseClause // command word -> its case clause
}

// parseMainFuncs parses main.go and returns its top-level function declarations by name.
func parseMainFuncs(t *testing.T) (*token.FileSet, map[string]*ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			funcs[fd.Name.Name] = fd
		}
	}
	return fset, funcs
}

// findDispatch returns the first top-level switch on args[0] in fn, with its string case labels.
// ok is false when fn has no such switch.
func findDispatch(fn *ast.FuncDecl) (dispatchSwitch, bool) {
	if fn == nil || fn.Body == nil {
		return dispatchSwitch{}, false
	}
	for _, st := range fn.Body.List {
		sw, ok := st.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			continue
		}
		if _, ok := sw.Tag.(*ast.IndexExpr); !ok { // args[0]
			continue
		}
		d := dispatchSwitch{pos: sw.Pos(), cases: map[string]*ast.CaseClause{}}
		for _, s := range sw.Body.List {
			cc, ok := s.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				word, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				d.cases[word] = cc
			}
		}
		return d, true
	}
	return dispatchSwitch{}, false
}

// calledFuncs returns the names of the plain functions called in a case clause's body.
func calledFuncs(cc *ast.CaseClause) map[string]bool {
	names := map[string]bool{}
	for _, s := range cc.Body {
		ast.Inspect(s, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
			return true
		})
	}
	return names
}

func TestRegistryCommandsAreDispatched(t *testing.T) {
	fset, funcs := parseMainFuncs(t)
	at := func(p token.Pos) string { return "main.go:" + strconv.Itoa(fset.Position(p).Line) }

	top, ok := findDispatch(funcs["run"])
	if !ok {
		t.Fatal("main.go: func run has no switch on args[0]; the check cannot see the dispatch")
	}
	if len(toolRegistry) == 0 {
		t.Fatal("toolRegistry is empty; nothing to check")
	}

	for _, e := range toolRegistry {
		words := strings.Fields(e.CLI)
		if len(words) == 0 || len(words) > 2 {
			t.Errorf("registry entry %q: want one or two command words", e.CLI)
			continue
		}
		cc, ok := top.cases[words[0]]
		if !ok {
			t.Errorf("registry entry %q: run() does not dispatch %q; add a case to the switch at %s",
				e.CLI, words[0], at(top.pos))
			continue
		}
		if len(words) == 1 {
			continue
		}
		dispatcher, known := subcommandDispatchers[words[0]]
		if !known {
			t.Errorf("registry entry %q: no second-level dispatcher is known for %q; add it to subcommandDispatchers",
				e.CLI, words[0])
			continue
		}
		if !calledFuncs(cc)[dispatcher] {
			t.Errorf("registry entry %q: the run() case %q at %s does not call %s",
				e.CLI, words[0], at(cc.Pos()), dispatcher)
			continue
		}
		sub, ok := findDispatch(funcs[dispatcher])
		if !ok {
			t.Errorf("registry entry %q: main.go: func %s has no switch on args[0]", e.CLI, dispatcher)
			continue
		}
		if _, ok := sub.cases[words[1]]; !ok {
			t.Errorf("registry entry %q: %s does not dispatch %q; add a case to the switch at %s",
				e.CLI, dispatcher, words[1], at(sub.pos))
		}
	}
}
