// Package wiregen writes the glue code that wires a unit into a Go module. It never overwrites a
// unit that exists, and never rewrites a registry it did not generate.
package wiregen

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-anything/go-anything/internal/usecase"
)

var (
	modulePattern = regexp.MustCompile(`(?m)^module\s+(\S+)`)
	prefixConst   = regexp.MustCompile(`const Prefix = "([^"]+)"`)
	kindConst     = regexp.MustCompile(`const Kind = "([^"]+)"`)
)

// Unit is a wired unit already present in the module.
type Unit struct {
	Name   string
	Kind   string // "http" or "cli"
	Prefix string // HTTP only
}

// ReadModule returns the module path from root/go.mod.
func ReadModule(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("wire: read go.mod: %w", err)
	}
	m := modulePattern.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("wire: go.mod has no module line")
	}
	return string(m[1]), nil
}

// ListUnits returns the units already wired under root/units.
func ListUnits(root string) ([]Unit, error) {
	dir := filepath.Join(root, "units")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wire: read %s: %w", dir, err)
	}
	var out []Unit
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "unit.go"))
		if err != nil {
			continue // a folder without unit.go is not a wired unit
		}
		k := kindConst.FindSubmatch(data)
		if k == nil {
			return nil, fmt.Errorf("wire: units/%s/unit.go has no Kind constant", e.Name())
		}
		u := Unit{Name: e.Name(), Kind: string(k[1])}
		if u.Kind == "http" {
			m := prefixConst.FindSubmatch(data)
			if m == nil {
				return nil, fmt.Errorf("wire: units/%s/unit.go has no Prefix constant", e.Name())
			}
			u.Prefix = string(m[1])
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Gen wires one unit into root and returns the paths it wrote. An empty prefix makes a command-line
// unit (its command is its name); a prefix makes an HTTP unit served under that URL path.
func Gen(root, unit, prefix string) ([]string, error) {
	if err := usecase.ValidUnitName(unit); err != nil {
		return nil, err
	}
	kind := "cli"
	if prefix != "" {
		kind = "http"
		if err := usecase.ValidPrefix(prefix); err != nil {
			return nil, err
		}
	}
	module, err := ReadModule(root)
	if err != nil {
		return nil, err
	}
	existing, err := ListUnits(root)
	if err != nil {
		return nil, err
	}
	unitDir := filepath.Join(root, "units", unit)
	if _, err := os.Stat(unitDir); err == nil {
		return nil, fmt.Errorf("wire: units/%s already exists; it is never overwritten", unit)
	}
	refs := []usecase.UnitRef{{Name: unit, Kind: kind}}
	for _, u := range existing {
		if u.Name == unit {
			return nil, fmt.Errorf("wire: unit %s is already wired", unit)
		}
		if kind == "http" && u.Kind == "http" && u.Prefix == prefix {
			return nil, fmt.Errorf("wire: prefix %s is already used by unit %s", prefix, u.Name)
		}
		refs = append(refs, usecase.UnitRef{Name: u.Name, Kind: u.Kind})
	}
	registryPath := filepath.Join(root, "units", "units.go")
	if err := checkRegistryOwnership(registryPath); err != nil {
		return nil, err
	}

	var files map[string][]byte
	if kind == "http" {
		files, err = usecase.UnitFiles(unit, prefix)
	} else {
		files, err = usecase.CLIUnitFiles(unit)
	}
	if err != nil {
		return nil, err
	}
	var written []string
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return nil, fmt.Errorf("wire: create %s: %w", unitDir, err)
	}
	for name, data := range files {
		p := filepath.Join(unitDir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return written, fmt.Errorf("wire: write %s: %w", p, err)
		}
		written = append(written, p)
	}
	registry, err := usecase.Registry(module, refs)
	if err != nil {
		return written, err
	}
	if err := os.WriteFile(registryPath, registry, 0o644); err != nil {
		return written, fmt.Errorf("wire: write registry: %w", err)
	}
	return append(written, registryPath), nil
}

// checkRegistryOwnership refuses to touch a units.go that was not generated.
func checkRegistryOwnership(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wire: read registry: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() && strings.HasPrefix(sc.Text(), usecase.RegistryMarker) {
		return nil
	}
	return fmt.Errorf("wire: %s exists and was not written by wire gen; move it aside to continue", path)
}
