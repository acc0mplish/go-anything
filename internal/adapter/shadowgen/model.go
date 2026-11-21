// Package shadowgen generates a drop-in shadow package for a Go library package. The shadow package
// exports the original's functions; at runtime it calls the original, the rewrite, or both and logs
// any disagreement, as chosen by an environment variable.
package shadowgen

import "go/types"

// Options say what to shadow and where to write the shadow package.
type Options struct {
	ModuleRoot string // directory with the go.mod that can import both packages; required
	Original   string // import path of the original package; required
	Rewrite    string // import path of the rewrite package; required
	OutDir     string // where the shadow package goes; default <ModuleRoot>/shadow/<package name>
	DryRun     bool   // analyze and report, write nothing
}

// Param is one parameter of an exported function.
type Param struct {
	Name string     // a name that is safe to use in the wrapper
	Type types.Type // for a variadic parameter this is the slice type
}

// Result is one result of an exported function.
type Result struct {
	Type    types.Type
	IsError bool // the result type is exactly the predeclared error interface
}

// Func is one exported top-level function of the original package with its full signature.
type Func struct {
	Name      string
	Signature *types.Signature
	Params    []Param
	Results   []Result
	Variadic  bool
	LastError bool // the last result is error
}

// Skip names something of the original that is not shadowed and why.
type Skip struct {
	Name        string
	Reason      string
	Passthrough bool // the shadow package still exports it and always calls the original
}

// Risk names a shadowed function whose arguments are shared by both calls.
type Risk struct {
	Name   string
	Reason string
}

// Report is what Generate found and did.
type Report struct {
	Package     string // package name of the original and of the shadow package
	Dir         string // directory of the shadow package
	EnvVar      string // GA_SHADOW_<PKG>
	Shadowed    []string
	NotShadowed []Skip
	Risks       []Risk
	Reexported  []string // exported types and constants that the shadow package aliases
	Files       []string // files written; empty on a dry run or on failure
	DryRun      bool
}

// Limits is the fixed statement of what a shadow package does not do. It is printed by the command
// and written into the generated file header.
var Limits = []string{
	"Methods are not shadowed. They come with the re-exported types and always run the original.",
	"Functions with func or channel parameters are not shadowed, because running their side effects twice is unsafe. They always call the original.",
	"Functions whose results hold func or channel values are not shadowed, because those results cannot be compared.",
	"Generic functions, generic types, functions whose signature uses an unexported type, and package variables are not part of the shadow package.",
	"In shadow mode both calls get the same arguments, the original first. A function that mutates a pointer, slice, map or interface argument, or reads from it, runs that effect twice, and the caller sees the argument as the rewrite left it. Such functions are listed as risks.",
	"Only results are compared. Changes a function makes to its arguments are not compared, so a function without results is checked only for a rewrite panic.",
	"Mismatch records contain argument and result values, which may include personal data. Set GA_SHADOW_REDACT=1 to log only the function name and time.",
}
