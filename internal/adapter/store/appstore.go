package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-anything/go-anything/internal/domain"
)

// SaveApp validates and writes an app recording atomically, like Save does for unit recordings.
func SaveApp(path string, rec domain.AppRecording) error {
	if err := rec.Validate(); err != nil {
		return fmt.Errorf("store: refuse to save app recording: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode app recording: %w", err)
	}
	return writeAtomic(path, append(data, '\n'))
}

// LoadApp reads and validates an app recording.
func LoadApp(path string) (domain.AppRecording, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.AppRecording{}, fmt.Errorf("store: read %s: %w", path, err)
	}
	var rec domain.AppRecording
	if err := json.Unmarshal(data, &rec); err != nil {
		return domain.AppRecording{}, fmt.Errorf("store: parse %s: %w", path, err)
	}
	if err := rec.Validate(); err != nil {
		return domain.AppRecording{}, fmt.Errorf("store: %s: %w", path, err)
	}
	return rec, nil
}

// writeAtomic writes via a temp file and rename, so a crash cannot leave a half-written file.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rec-*.json")
	if err != nil {
		return fmt.Errorf("store: temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("store: chmod: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}
