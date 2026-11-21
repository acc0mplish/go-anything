package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/go-anything/go-anything/internal/adapter/shadowgen"
)

// cmdShadowGen writes a drop-in shadow package for a Go library package: callers import it instead of the
// original and choose at runtime, with GA_SHADOW_<PKG>=old|shadow|new, whether the original, the rewrite or
// both run. Everything it does not shadow is listed, never skipped silently.
func cmdShadowGen(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("shadow gen", flag.ContinueOnError)
	moduleRoot := fs.String("module-root", ".", "directory with the go.mod that can import both packages")
	original := fs.String("original", "", "import path of the original package, required")
	rewrite := fs.String("rewrite", "", "import path of the rewrite package, required")
	out := fs.String("out", "", "output directory (default <module-root>/shadow/<package name>); must be missing or empty")
	dryRun := fs.Bool("dry-run", false, "list what would be generated and write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("shadow gen: unexpected arguments %q", fs.Args())
	}
	if *original == "" || *rewrite == "" {
		return errors.New("shadow gen: -original and -rewrite are required")
	}
	rep, err := shadowgen.Generate(shadowgen.Options{
		ModuleRoot: *moduleRoot, Original: *original, Rewrite: *rewrite, OutDir: *out, DryRun: *dryRun,
	})
	if rep.Package != "" {
		printShadowReport(stdout, rep, *original)
	}
	if err != nil {
		return err
	}
	if rep.DryRun {
		fmt.Fprintf(stdout, "dry run: nothing written; would write %s and %s in %s\n",
			shadowgen.FuncsFile, shadowgen.RuntimeFile, rep.Dir)
		return nil
	}
	for _, f := range rep.Files {
		fmt.Fprintf(stdout, "wrote: %s\n", f)
	}
	fmt.Fprintf(stdout, "built and vetted; import the package at %s instead of %s\n", filepath.ToSlash(rep.Dir), *original)
	return nil
}

func printShadowReport(w io.Writer, rep shadowgen.Report, original string) {
	fmt.Fprintf(w, "shadow gen: package %s (%s) -> %s\n", rep.Package, original, rep.Dir)
	fmt.Fprintf(w, "mode: %s=old|shadow|new (default old); mismatch log: GA_SHADOW_LOG (default stderr)\n", rep.EnvVar)
	for _, name := range rep.Shadowed {
		fmt.Fprintf(w, "shadowed: %s\n", name)
	}
	for _, s := range rep.NotShadowed {
		fmt.Fprintf(w, "not shadowed: %s: %s\n", s.Name, s.Reason)
	}
	for _, r := range rep.Risks {
		fmt.Fprintf(w, "risk: %s: %s\n", r.Name, r.Reason)
	}
	if len(rep.Reexported) > 0 {
		fmt.Fprintf(w, "re-exported: %s\n", strings.Join(rep.Reexported, ", "))
	}
	for _, l := range shadowgen.Limits {
		fmt.Fprintf(w, "limit: %s\n", l)
	}
}
