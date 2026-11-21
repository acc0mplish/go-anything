package layout

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Binary is one executable a check build produced.
type Binary struct {
	Name   string
	Size   int64
	Format string // what the file header says, e.g. "ELF arm64"; empty when the format is not inspected
	Match  bool   // the header agrees with the target's GOOS/GOARCH (true when not inspected)
}

// TargetResult is the outcome of building every main for one target.
type TargetResult struct {
	Target   Target
	OK       bool
	Duration time.Duration
	Binaries []Binary
	ErrTail  string // last lines of go build output when it failed
}

// Check builds every main package for every planned target into a temporary directory and deletes
// it afterwards. Without cgo the builds use CGO_ENABLED=0, which is what makes one machine enough for
// all platforms. Nothing is published or kept. progress, when not nil, sees each result as it lands.
func Check(ctx context.Context, p *LayoutPlan, progress func(TargetResult)) ([]TargetResult, error) {
	tmp, err := os.MkdirTemp("", "go-anything-package-check-*")
	if err != nil {
		return nil, fmt.Errorf("layout: check: %w", err)
	}
	defer os.RemoveAll(tmp)

	var results []TargetResult
	for _, t := range p.Targets {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		r := checkTarget(ctx, p, t, filepath.Join(tmp, t.GOOS+"_"+t.GOARCH))
		results = append(results, r)
		if progress != nil {
			progress(r)
		}
	}
	return results, nil
}

func checkTarget(ctx context.Context, p *LayoutPlan, t Target, outDir string) TargetResult {
	r := TargetResult{Target: t}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		r.ErrTail = err.Error()
		return r
	}
	args := []string{"build", "-trimpath", "-ldflags", "-s -w -X main.version=v0.0.0-check", "-o", outDir + string(filepath.Separator)}
	for _, m := range p.Mains {
		args = append(args, pkgArg(m))
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = p.Root
	cmd.Env = append(os.Environ(), "GOOS="+t.GOOS, "GOARCH="+t.GOARCH)
	if !p.UsesCgo {
		cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	r.Duration = time.Since(start)
	if err != nil {
		r.ErrTail = tail(out.String()+err.Error(), 15)
		return r
	}
	r.OK = true
	for _, m := range p.Mains {
		name := m.BinName
		if t.GOOS == "windows" {
			name += ".exe"
		}
		b := Binary{Name: name, Match: true}
		fi, err := os.Stat(filepath.Join(outDir, name))
		if err != nil {
			r.OK = false
			r.ErrTail = fmt.Sprintf("go build succeeded but %s is missing: %v", name, err)
			continue
		}
		b.Size = fi.Size()
		b.Format, b.Match = inspect(filepath.Join(outDir, name), t)
		if !b.Match {
			r.OK = false
			r.ErrTail = fmt.Sprintf("%s is %s, not %s", name, b.Format, t)
		}
		r.Binaries = append(r.Binaries, b)
	}
	return r
}

// inspect reads the executable header so a check result says what was really produced.
func inspect(file string, t Target) (string, bool) {
	if f, err := elf.Open(file); err == nil {
		defer f.Close()
		arch := map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64", elf.EM_386: "386", elf.EM_ARM: "arm", elf.EM_RISCV: "riscv64"}[f.Machine]
		if arch == "" {
			return "ELF " + f.Machine.String() + " (not compared)", true
		}
		return "ELF " + arch, t.GOOS != "darwin" && t.GOOS != "windows" && arch == t.GOARCH
	}
	if f, err := macho.Open(file); err == nil {
		defer f.Close()
		arch := map[macho.Cpu]string{macho.CpuAmd64: "amd64", macho.CpuArm64: "arm64"}[f.Cpu]
		if arch == "" {
			return "Mach-O " + f.Cpu.String() + " (not compared)", true
		}
		return "Mach-O " + arch, (t.GOOS == "darwin" || t.GOOS == "ios") && arch == t.GOARCH
	}
	if f, err := pe.Open(file); err == nil {
		defer f.Close()
		arch := map[uint16]string{pe.IMAGE_FILE_MACHINE_AMD64: "amd64", pe.IMAGE_FILE_MACHINE_ARM64: "arm64", pe.IMAGE_FILE_MACHINE_I386: "386"}[f.Machine]
		if arch == "" {
			return fmt.Sprintf("PE machine %#x (not compared)", f.Machine), true
		}
		return "PE " + arch, t.GOOS == "windows" && arch == t.GOARCH
	}
	// wasm, plan9 and others: the build succeeded; the header is not inspected.
	return "", true
}
