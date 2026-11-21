package domain

// Package describes one Go package found under the analyzed root.
//
// Files, Imports, Funcs, Exported and Lines come from the syntax of every non-test file and are
// always present. The fields after Tests come from type checking the files that match the
// current build context; they are optional, so readers of older analyses keep working.
type Package struct {
	ImportPath string   `json:"import_path"`
	Dir        string   `json:"dir"`
	Files      []string `json:"files"`
	Imports    []string `json:"imports"`
	Funcs      []string `json:"funcs"`
	Exported   []string `json:"exported"`
	Internal   bool     `json:"internal"`
	// Lines counts source lines of the package's non-test Go files.
	Lines int `json:"lines"`
	// Tests counts the package's _test.go files: behavior that is already checked by its own tests.
	Tests int `json:"tests"`

	// Name is the package clause name; "main" marks a command.
	Name string `json:"name,omitempty"`
	// Types are the package-level type declarations.
	Types []TypeInfo `json:"types,omitempty"`
	// Functions are the functions and methods with their signature and call relations.
	// The name differs from Funcs, which stays the plain list of names.
	Functions []FuncInfo `json:"functions,omitempty"`
	// RiskSites are the places that make the package harder to port, each with file:line.
	RiskSites []RiskSite `json:"risk_sites,omitempty"`
	// TypeErrors holds "file:line: message" for a package that failed to type-check. The syntax
	// level data above is still valid; the call data of such a package may be incomplete.
	TypeErrors []string `json:"type_errors,omitempty"`
	// BuildIgnored lists files left out of type checking because their build constraints do not
	// match the current GOOS, GOARCH and tags.
	BuildIgnored []string `json:"build_ignored,omitempty"`

	// GoPackage is the suggested Go package path for the rewrite of a unit of a non-Go original,
	// such as internal/shop/price for the Python module shop.price. Empty for a Go analysis.
	GoPackage string `json:"go_package,omitempty"`
}

// TypeInfo is one package-level type declaration.
type TypeInfo struct {
	Name string `json:"name"`
	// Kind is struct, interface, named (any other defined type) or alias.
	Kind string `json:"kind"`
	// Methods are the declared methods: the method set written in the package for a struct or
	// named type, the explicit methods for an interface.
	Methods []string `json:"methods,omitempty"`
	Pos     string   `json:"pos"`
}

// FuncInfo is one function or method with what a static walk of its body can see.
type FuncInfo struct {
	Name string `json:"name"`
	// Receiver is the receiver type for a method, with "*" for a pointer receiver.
	Receiver string `json:"receiver,omitempty"`
	// Signature is the type of the function written relative to its own package.
	Signature string `json:"signature,omitempty"`
	Pos       string `json:"pos"`
	// Callees are the in-module functions this one calls statically, as FuncID values.
	Callees []string `json:"callees,omitempty"`
	// Callers counts the distinct in-module functions that call this one statically.
	Callers int `json:"callers"`
	// Refs counts in-module uses of the function as a value (not a call), such as a callback.
	Refs int `json:"refs,omitempty"`
	// DynamicCalls counts calls a static walk cannot resolve: interface methods and function values.
	DynamicCalls int `json:"dynamic_calls,omitempty"`
	// GoStatements counts go statements in the body.
	GoStatements int `json:"go_statements,omitempty"`
	// Exported is true when the function can be used from another package: its name is exported
	// and, for a method, so is the receiver type.
	Exported bool `json:"exported"`
	// PlainSignature is true when every parameter, result and receiver is a basic type, error, a
	// module-local struct or basic-based type, or a pointer, slice, array or map of those.
	PlainSignature bool `json:"plain_signature,omitempty"`
}

// RiskSite is one place in the source that makes porting harder.
type RiskSite struct {
	// Kind is one of the Risk* constants.
	Kind   string `json:"kind"`
	Pos    string `json:"pos"`
	Detail string `json:"detail,omitempty"`
}

// Kinds of RiskSite.
const (
	RiskCgo           = "cgo"
	RiskUnsafe        = "unsafe"
	RiskGlobalMutable = "global_mutable"
	RiskGoroutine     = "goroutine"
	RiskReflect       = "reflect"
	RiskInit          = "init"

	// Kinds found in non-Go originals. Each names what the code does, not how Go would do it.

	// RiskConcurrency is threads, processes or an event loop (threading, asyncio, worker_threads).
	RiskConcurrency = "concurrency"
	// RiskDynamicCode is code built or loaded at run time (eval, exec, dynamic import).
	RiskDynamicCode = "dynamic_code"
	// RiskNativeCode is native code reached through an FFI (ctypes, cffi, .node addons).
	RiskNativeCode = "native_code"
	// RiskSideEffects is starting other processes (subprocess, os.system, child_process).
	RiskSideEffects = "side_effects"
	// RiskNetwork is sockets, HTTP clients and servers.
	RiskNetwork = "network"
	// RiskFSWrite is writing, moving or deleting files.
	RiskFSWrite = "fs_write"
	// RiskUnsafeDeserialize is deserialization that can run code (pickle).
	RiskUnsafeDeserialize = "unsafe_deserialization"
	// RiskNondeterminism is clock and random values, which change between runs.
	RiskNondeterminism = "nondeterminism"
	// RiskImportCycle is an import that is part of a cycle between units. Go forbids import cycles.
	RiskImportCycle = "import_cycle"
)

// StartItem is one function that is a good first port: a leaf with a plain signature.
type StartItem struct {
	Func      string `json:"func"`
	Package   string `json:"package"`
	Pos       string `json:"pos"`
	Callers   int    `json:"callers"`
	Signature string `json:"signature,omitempty"`
	// Importers counts the units of the analyzed tree that import the function's unit. It is set
	// for non-Go originals, where the list is ranked by it.
	Importers int `json:"importers,omitempty"`
}

// Analysis is the plan-level view of a source tree.
type Analysis struct {
	Root     string    `json:"root"`
	Module   string    `json:"module"`
	Packages []Package `json:"packages"`
	// Language is the language of the analyzed source: go, python or js. A non-Go original has
	// one Package per module (file), with ImportPath in the original's own notation.
	Language string `json:"language,omitempty"`
	// Limits states what the analyzer of a non-Go original cannot see, so readers can judge the
	// data. Empty for Go.
	Limits []string `json:"limits,omitempty"`

	// CallEdges counts distinct static caller to callee pairs inside the module.
	CallEdges int `json:"call_edges,omitempty"`
	// DynamicCalls counts call sites that a static walk cannot resolve.
	DynamicCalls int `json:"dynamic_calls,omitempty"`
	// Unreached lists exported package-level functions outside package main that nothing in the
	// module calls or references. Tests are not part of the walk.
	Unreached []string `json:"unreached,omitempty"`
	// StartHere ranks the best first function ports, most used first.
	StartHere []StartItem `json:"start_here,omitempty"`
}

// FuncID is the qualified name used in Callees, StartHere and Unreached:
// "path.Func" for a function and "path.Type.Method" for a method. A leading "*" on the
// receiver is dropped, so pointer and value receivers share one name space as in Go.
func FuncID(importPath, receiver, name string) string {
	if receiver == "" {
		return importPath + "." + name
	}
	if receiver[0] == '*' {
		receiver = receiver[1:]
	}
	return importPath + "." + receiver + "." + name
}
