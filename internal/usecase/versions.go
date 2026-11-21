package usecase

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// versionDirPattern matches a version folder name such as v1 or v12.
var versionDirPattern = regexp.MustCompile(`^v([0-9]+)$`)

// ReportRoot is where reports live inside an analyzed project.
const ReportRoot = ".go-anything/reports"

// NextVersion returns the number for a new run of a subject: one more than the highest vN
// already present, or 1. It only looks at the names; it never removes or changes anything.
func NextVersion(subjectDir string) (int, error) {
	entries, err := os.ReadDir(subjectDir)
	if errors.Is(err, os.ErrNotExist) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("versions: read %s: %w", subjectDir, err)
	}
	highest := 0
	for _, e := range entries {
		m := versionDirPattern.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest + 1, nil
}

// VersionDir creates and returns the folder for version n of a subject under root:
// <root>/.go-anything/reports/<subject>/v<n>. It refuses to reuse an existing folder, so a
// version can never be overwritten.
func VersionDir(root, subject string, n int) (string, error) {
	if n < 1 {
		return "", fmt.Errorf("versions: version must be >= 1, got %d", n)
	}
	base := filepath.Join(root, ReportRoot, subject)
	dir := filepath.Join(base, fmt.Sprintf("v%d", n))
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("versions: %s already exists; refusing to overwrite a previous version", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("versions: create %s: %w", dir, err)
	}
	return dir, nil
}
