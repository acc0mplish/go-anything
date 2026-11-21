package domain

import (
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprint identifies the exact source files behind a recording. It changes when any
// file's bytes or the set of files changes, so a stale recording cannot pass silently.
type Fingerprint struct {
	Files int    `json:"files"`
	Hash  string `json:"hash"`
}

// skipDirs are never part of a unit's behavior.
var skipDirs = map[string]bool{".git": true, "vendor": true, "testdata": true}

// FingerprintDir hashes every .go file under dir (path and bytes, in sorted order).
func FingerprintDir(dir string) (Fingerprint, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return Fingerprint{}, fmt.Errorf("fingerprint: walk %s: %w", dir, err)
	}
	sort.Strings(files)
	h := fnv.New64a()
	for _, f := range files {
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			return Fingerprint{}, fmt.Errorf("fingerprint: rel %s: %w", f, err)
		}
		// Separate path and content with NUL so renames and edits cannot cancel out.
		io.WriteString(h, filepath.ToSlash(rel)+"\x00")
		fh, err := os.Open(f)
		if err != nil {
			return Fingerprint{}, fmt.Errorf("fingerprint: open %s: %w", f, err)
		}
		if _, err := io.Copy(h, fh); err != nil {
			fh.Close()
			return Fingerprint{}, fmt.Errorf("fingerprint: read %s: %w", f, err)
		}
		fh.Close()
		io.WriteString(h, "\x00")
	}
	return Fingerprint{Files: len(files), Hash: fmt.Sprintf("%016x", h.Sum64())}, nil
}

// Drifted reports whether the source now differs from the recorded fingerprint.
func (f Fingerprint) Drifted(now Fingerprint) bool {
	return f.Hash != now.Hash || f.Files != now.Files
}
