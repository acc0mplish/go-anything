// Package routesfile reads and changes the routing file the front door follows. Editing the file is
// how a unit is wired to the rewrite or back to the original: the front door reloads it without a
// restart.
package routesfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-anything/go-anything/internal/domain"
)

// File is the on-disk shape.
type File struct {
	Routes []domain.Route `json:"routes"`
}

// Load reads and validates a routing file.
func Load(path string) ([]domain.Route, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("routes: read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("routes: parse %s: %w", path, err)
	}
	if err := domain.ValidRoutes(f.Routes); err != nil {
		return nil, fmt.Errorf("routes: %s: %w", path, err)
	}
	return f.Routes, nil
}

// Units lists each unit with the target it currently points at. A unit whose routes disagree is
// reported as "mixed", because that is almost always a mistake.
func Units(routes []domain.Route) map[string]string {
	out := map[string]string{}
	for _, r := range routes {
		if r.Unit == "" {
			continue
		}
		if prev, ok := out[r.Unit]; ok && prev != r.Target {
			out[r.Unit] = "mixed"
			continue
		}
		if _, ok := out[r.Unit]; !ok {
			out[r.Unit] = r.Target
		}
	}
	return out
}

// Set switches every route of one unit to target, then writes the file atomically: a crash cannot
// leave a half-written routing file for the front door to read.
func Set(path, unit, target string) (int, error) {
	if target != domain.TargetRewrite && target != domain.TargetOriginal {
		return 0, fmt.Errorf("routes: target must be %s or %s", domain.TargetRewrite, domain.TargetOriginal)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("routes: read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("routes: parse %s: %w", path, err)
	}
	changed := 0
	for i := range f.Routes {
		if f.Routes[i].Unit == unit {
			f.Routes[i].Target = target
			changed++
		}
	}
	if changed == 0 {
		return 0, errors.New("routes: no route belongs to unit " + unit)
	}
	if err := domain.ValidRoutes(f.Routes); err != nil {
		return 0, fmt.Errorf("routes: refusing to write an invalid file: %w", err)
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("routes: encode: %w", err)
	}
	if err := writeAtomic(path, append(out, '\n')); err != nil {
		return 0, err
	}
	return changed, nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".routes-*.json")
	if err != nil {
		return fmt.Errorf("routes: temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("routes: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("routes: close: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("routes: chmod: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("routes: rename: %w", err)
	}
	return nil
}
