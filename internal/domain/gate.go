package domain

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// GateCheck is one completion criterion and whether it passed.
type GateCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// GatePassed reports whether every check passed and at least one ran.
func GatePassed(checks []GateCheck) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// Finding is one source location that breaks a rule.
type Finding struct {
	File   string
	Line   int
	Rule   string
	Detail string
}

// placeholderMessages are the panic messages a finished port must not use as a body.
var placeholderMessages = []string{"not implemented", "unimplemented", "TODO", "todo"}

// ScanUnimplemented reports placeholder bodies in non-test Go source: a call to panic whose message
// is a placeholder. It reads the syntax tree, so the same words inside a string or comment are not
// mistaken for code (a template that contains a placeholder is not a placeholder).
func ScanUnimplemented(file string, src string) []Finding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return []Finding{{File: file, Line: 1, Rule: "unparseable", Detail: err.Error()}}
	}
	var out []Finding
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "panic" || len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		msg, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, p := range placeholderMessages {
			if strings.HasPrefix(msg, p) {
				pos := fset.Position(call.Pos())
				out = append(out, Finding{File: file, Line: pos.Line, Rule: "unimplemented", Detail: "panic(" + strconv.Quote(msg) + ")"})
				break
			}
		}
		return true
	})
	return out
}

// ScanUnsafe requires every use of the unsafe package (unsafe.Pointer, unsafe.Sizeof, ...) to be
// justified by a "SAFETY:" comment on the same line or the line above. It reads the syntax tree, so
// a mention of unsafe in a comment or a string is not a use.
func ScanUnsafe(file string, src string) []Finding {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return []Finding{{File: file, Line: 1, Rule: "unparseable", Detail: err.Error()}}
	}
	safetyLines := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "SAFETY:") {
				for l := fset.Position(c.Pos()).Line; l <= fset.Position(c.End()).Line; l++ {
					safetyLines[l] = true
				}
			}
		}
	}
	var out []Finding
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != "unsafe" {
			return true
		}
		line := fset.Position(sel.Pos()).Line
		if safetyLines[line] || safetyLines[line-1] {
			return true
		}
		out = append(out, Finding{File: file, Line: line, Rule: "unsafe-without-safety", Detail: "unsafe." + sel.Sel.Name})
		return true
	})
	return out
}
