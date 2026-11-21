package shadowgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Generate analyzes the original and rewrite packages and writes the shadow package. It refuses to write
// into a directory that already has content. After writing it builds the module that holds the shadow
// package and vets the package; if either fails it removes what it wrote and returns the error.
func Generate(opts Options) (Report, error) {
	if opts.ModuleRoot == "" || opts.Original == "" || opts.Rewrite == "" {
		return Report{}, errors.New("shadow gen: module root, original and rewrite are required")
	}
	if opts.Original == opts.Rewrite {
		return Report{}, errors.New("shadow gen: original and rewrite are the same package")
	}
	root, err := filepath.Abs(opts.ModuleRoot)
	if err != nil {
		return Report{}, fmt.Errorf("shadow gen: module root: %w", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return Report{}, fmt.Errorf("shadow gen: %s has no go.mod: %w", root, err)
	}
	orig, rew, err := loadPackages(root, opts.Original, opts.Rewrite)
	if err != nil {
		return Report{}, err
	}
	a := analyze(orig, rew)
	rep := a.report
	rep.DryRun = opts.DryRun
	out := opts.OutDir
	if out == "" {
		out = filepath.Join(root, "shadow", orig.Name())
	}
	if out, err = filepath.Abs(out); err != nil {
		return rep, fmt.Errorf("shadow gen: output dir: %w", err)
	}
	rep.Dir = out
	if err := checkEmptyDir(out); err != nil {
		return rep, err
	}
	buildRoot, err := enclosingModule(out)
	if err != nil {
		return rep, err
	}
	if len(a.funcs) == 0 {
		return rep, fmt.Errorf("shadow gen: no function of %s can be shadowed; see the not-shadowed list", opts.Original)
	}
	funcsSrc, runtimeSrc, err := render(a, opts.Original, opts.Rewrite)
	if err != nil {
		return rep, err
	}
	if opts.DryRun {
		return rep, nil
	}

	created, err := mkdirTracked(out)
	if err != nil {
		return rep, err
	}
	var written []string
	cleanup := func() {
		for _, f := range written {
			_ = os.Remove(f)
		}
		for i := len(created) - 1; i >= 0; i-- {
			_ = os.Remove(created[i]) // only succeeds while the directory is empty
		}
	}
	for _, f := range []struct {
		name string
		src  []byte
	}{{FuncsFile, funcsSrc}, {RuntimeFile, runtimeSrc}} {
		p := filepath.Join(out, f.name)
		// O_EXCL: never replace a file, even one that appeared after the empty check.
		fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			cleanup()
			return rep, fmt.Errorf("shadow gen: create %s: %w", p, err)
		}
		written = append(written, p)
		_, werr := fh.Write(f.src)
		cerr := fh.Close()
		if werr != nil || cerr != nil {
			cleanup()
			return rep, fmt.Errorf("shadow gen: write %s: %w", p, errors.Join(werr, cerr))
		}
	}
	rel, err := filepath.Rel(buildRoot, out)
	if err != nil {
		cleanup()
		return rep, fmt.Errorf("shadow gen: %w", err)
	}
	pkgArg := "./" + filepath.ToSlash(rel)
	if err := runGo(buildRoot, "build", "./..."); err != nil {
		cleanup()
		return rep, err
	}
	if err := runGo(buildRoot, "vet", pkgArg); err != nil {
		cleanup()
		return rep, err
	}
	rep.Files = written
	return rep, nil
}

// checkEmptyDir allows a missing or empty directory and refuses anything else.
func checkEmptyDir(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("shadow gen: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("shadow gen: %s exists and is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("shadow gen: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("shadow gen: %s already has content; refusing to overwrite it (remove it or pass another output dir)", dir)
	}
	return nil
}

// enclosingModule finds the nearest directory at or above dir with a go.mod.
func enclosingModule(dir string) (string, error) {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("shadow gen: output dir %s is not inside a Go module", dir)
		}
		d = parent
	}
}

// mkdirTracked creates dir and its missing parents and returns the ones it created, outermost first.
func mkdirTracked(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("shadow gen: %w", err)
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	var created []string
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil {
			for j := len(created) - 1; j >= 0; j-- {
				_ = os.Remove(created[j])
			}
			return nil, fmt.Errorf("shadow gen: %w", err)
		}
		created = append(created, missing[i])
	}
	return created, nil
}

func runGo(dir string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shadow gen: go %s in %s failed, generated files removed: %v\n%s",
			strings.Join(args, " "), dir, err, strings.TrimSpace(out.String()))
	}
	return nil
}
