package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/go-anything/go-anything/internal/adapter/layout"
)

const packageUsage = `usage: go-anything package <module root> [-targets linux/amd64,...] [-dry-run] [-check]
  Lays out a Go module for installing and shipping: .github/workflows/release.yml (tag v* builds
  archives and SHA256SUMS), scripts/build-all.sh (the same cross build into dist/) and docs/INSTALL.md
  (go install lines). Existing files are kept, never overwritten.
  -dry-run  print the plan and the files it would create; write nothing
  -check    cross-compile every main for every target in a temp dir and report each; write nothing
  Nothing is ever published.`

// cmdPackage plans the install and release layout of a module, then writes it, prints it (-dry-run)
// or proves every target builds (-check).
func cmdPackage(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targets := fs.String("targets", "", "comma-separated GOOS/GOARCH list (default linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64)")
	dryRun := fs.Bool("dry-run", false, "print the plan only")
	check := fs.Bool("check", false, "cross-compile every target into a temp dir; write nothing")

	root := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		root, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, packageUsage)
			return nil
		}
		return fmt.Errorf("package: %w\n%s", err, packageUsage)
	}
	switch rest := fs.Args(); {
	case len(rest) == 1 && root == "":
		root = rest[0]
	case len(rest) > 0:
		return fmt.Errorf("package: unexpected arguments %q\n%s", rest, packageUsage)
	}
	if root == "" {
		return errors.New(packageUsage)
	}

	var opts layout.Options
	if *targets != "" {
		ts, err := layout.ParseTargets(*targets)
		if err != nil {
			return fmt.Errorf("package: %w", err)
		}
		opts.Targets = ts
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	plan, err := layout.Plan(ctx, root, opts)
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}
	printPlan(stdout, plan, *dryRun && !*check)

	switch {
	case *check:
		return runPackageCheck(ctx, stdout, plan)
	case *dryRun:
		fmt.Fprintln(stdout, "dry run: nothing written.")
		return nil
	}
	res, err := layout.Write(plan)
	for _, r := range res {
		if r.Created {
			fmt.Fprintf(stdout, "created %s\n", r.Path)
		} else {
			fmt.Fprintf(stdout, "kept    %s (already exists, not overwritten)\n", r.Path)
		}
	}
	if err != nil {
		return fmt.Errorf("package: %w", err)
	}
	return nil
}

func printPlan(w io.Writer, p *layout.LayoutPlan, contents bool) {
	fmt.Fprintf(w, "module  %s (go %s)\n", p.Module, p.GoVersion)
	fmt.Fprintln(w, "binaries:")
	for _, m := range p.Mains {
		fmt.Fprintf(w, "  %-20s %s (dir %s)\n", m.BinName, m.ImportPath, m.Dir)
	}
	if p.UsesCgo {
		fmt.Fprintf(w, "cgo     yes: %s\n", strings.Join(p.CgoFiles, ", "))
		fmt.Fprintln(w, "        cross builds need CGO_ENABLED=0, which cgo code cannot use; only the host target is planned")
	} else {
		fmt.Fprintln(w, "cgo     no (CGO_ENABLED=0 cross builds)")
	}
	ts := make([]string, len(p.Targets))
	for i, t := range p.Targets {
		ts[i] = t.String()
	}
	fmt.Fprintf(w, "targets %s\n", strings.Join(ts, " "))
	fmt.Fprintln(w, "files:")
	for _, f := range p.Files {
		action := "create"
		if f.Exists {
			action = "keep  "
		}
		fmt.Fprintf(w, "  %s %s (%d lines)\n", action, f.Path, strings.Count(f.Content, "\n"))
	}
	if len(p.Notes) > 0 {
		fmt.Fprintln(w, "notes:")
		for _, n := range p.Notes {
			fmt.Fprintf(w, "  - %s\n", n)
		}
	}
	if !contents {
		return
	}
	for _, f := range p.Files {
		if f.Exists {
			continue
		}
		fmt.Fprintf(w, "\n----- %s -----\n%s", f.Path, f.Content)
	}
	fmt.Fprintln(w)
}

func runPackageCheck(ctx context.Context, w io.Writer, p *layout.LayoutPlan) error {
	if p.UsesCgo {
		fmt.Fprintln(w, "check: cgo is used, so only the host target is built, with the toolchain's CGO_ENABLED")
	} else {
		fmt.Fprintln(w, "check: building every main for every target with CGO_ENABLED=0 into a temp dir")
	}
	results, err := layout.Check(ctx, p, func(r layout.TargetResult) {
		status := "ok    "
		if !r.OK {
			status = "FAILED"
		}
		fmt.Fprintf(w, "  %s %-15s %6.1fs", status, r.Target, r.Duration.Seconds())
		for _, b := range r.Binaries {
			fmt.Fprintf(w, "  %s %.1f MB %s", b.Name, float64(b.Size)/1e6, b.Format)
		}
		fmt.Fprintln(w)
		if !r.OK {
			for _, l := range strings.Split(r.ErrTail, "\n") {
				fmt.Fprintf(w, "         | %s\n", l)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("package: check: %w", err)
	}
	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	fmt.Fprintf(w, "check: %d of %d targets built; temp output removed; nothing written or published\n", len(results)-failed, len(results))
	if failed > 0 {
		return fmt.Errorf("package: check: %d target(s) failed to build", failed)
	}
	return nil
}
