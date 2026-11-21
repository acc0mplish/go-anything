// Package store is the filesystem adapter that implements usecase.RecordingStore.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-anything/go-anything/internal/domain"
)

// FileStore stores recordings as JSON files.
type FileStore struct{}

// Load reads and validates a recording file.
func (FileStore) Load(path string) (domain.Recording, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Recording{}, fmt.Errorf("store: read %s: %w", path, err)
	}
	var rec domain.Recording
	if err := json.Unmarshal(data, &rec); err != nil {
		return domain.Recording{}, fmt.Errorf("store: parse %s: %w", path, err)
	}
	if err := rec.Validate(); err != nil {
		return domain.Recording{}, fmt.Errorf("store: %s: %w", path, err)
	}
	return rec, nil
}

// Save validates and writes a recording atomically (temp file + rename).
func (FileStore) Save(path string, rec domain.Recording) error {
	if err := rec.Validate(); err != nil {
		return fmt.Errorf("store: refuse to save: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".rec-*.json")
	if err != nil {
		return fmt.Errorf("store: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("store: chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}
