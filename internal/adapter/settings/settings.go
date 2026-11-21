// Package settings reads a project's go-anything settings file. The file is .go-anything/config.json under
// the module root, and it sets the defect policy the operator chose for the project.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/go-anything/go-anything/internal/usecase"
)

const (
	dirName  = ".go-anything"
	fileName = "config.json"
)

// file is the on-disk shape. The pointer distinguishes an absent defect_policy from an empty one.
type file struct {
	DefectPolicy     *string           `json:"defect_policy"`
	UnitDefectPolicy map[string]string `json:"unit_defect_policy"`
}

// Path returns where a project's settings file lives. It is read whether or not the file exists.
func Path(root string) string {
	return filepath.Join(root, dirName, fileName)
}

// Load reads the defect policy settings of the project at root. A missing file is not an error: it
// returns no settings, so the built-in default applies. Any other problem is an error that names the file.
func Load(root string) (usecase.PolicySettings, error) {
	path := Path(root)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return usecase.PolicySettings{}, nil
	}
	if err != nil {
		return usecase.PolicySettings{}, fmt.Errorf("settings: read %s: %w", path, err)
	}
	return parse(path, data)
}

// allowedKeys are the only top-level keys. Unknown keys are refused, not ignored: a misspelt key such as
// "defect_polcy" would otherwise be skipped and the default would apply without any sign of the mistake.
var allowedKeys = map[string]bool{"defect_policy": true, "unit_defect_policy": true}

func parse(path string, data []byte) (usecase.PolicySettings, error) {
	// The keys are checked first, in their own pass, so the error can name the key itself.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return usecase.PolicySettings{}, fmt.Errorf("settings: %s: not a JSON object: %w", path, err)
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowedKeys[k] {
			return usecase.PolicySettings{}, fmt.Errorf("settings: %s: unknown key %q; the allowed keys are \"defect_policy\" and \"unit_defect_policy\"", path, k)
		}
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return usecase.PolicySettings{}, fmt.Errorf("settings: %s: %w", path, err)
	}
	var out usecase.PolicySettings
	if f.DefectPolicy != nil {
		p, err := parseValue(path, "defect_policy", *f.DefectPolicy)
		if err != nil {
			return usecase.PolicySettings{}, err
		}
		out.Default = &p
	}
	units := make([]string, 0, len(f.UnitDefectPolicy))
	for u := range f.UnitDefectPolicy {
		units = append(units, u)
	}
	sort.Strings(units)
	for _, u := range units {
		p, err := parseValue(path, "unit_defect_policy."+u, f.UnitDefectPolicy[u])
		if err != nil {
			return usecase.PolicySettings{}, err
		}
		if out.Units == nil {
			out.Units = map[string]usecase.DefectPolicy{}
		}
		out.Units[u] = p
	}
	return out, nil
}

// parseValue checks one policy value from the file. The empty string is refused here even though
// usecase.ParseDefectPolicy accepts it as the default: in a file it can only be a mistake.
func parseValue(path, key, value string) (usecase.DefectPolicy, error) {
	if value == "" {
		return "", fmt.Errorf("settings: %s: %q is empty; the valid values are \"defect\" and \"expected\"", path, key)
	}
	p, err := usecase.ParseDefectPolicy(value)
	if err != nil {
		return "", fmt.Errorf("settings: %s: %q: %w", path, key, err)
	}
	return p, nil
}
