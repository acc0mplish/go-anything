package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileResult says what happened to one planned file.
type FileResult struct {
	Path    string
	Created bool // false: the file already existed and was kept
}

// Write creates the planned files that do not exist yet. An existing file is never opened for
// writing: creation uses O_EXCL, so a file that appeared after Plan is kept too.
func Write(p *LayoutPlan) ([]FileResult, error) {
	var out []FileResult
	for _, f := range p.Files {
		if f.Exists {
			out = append(out, FileResult{Path: f.Path})
			continue
		}
		full := filepath.Join(p.Root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return out, fmt.Errorf("layout: %s: %w", f.Path, err)
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		fh, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			out = append(out, FileResult{Path: f.Path})
			continue
		}
		if err != nil {
			return out, fmt.Errorf("layout: %s: %w", f.Path, err)
		}
		_, werr := fh.WriteString(f.Content)
		cerr := fh.Close()
		if err := errors.Join(werr, cerr); err != nil {
			_ = os.Remove(full) // do not leave a half-written file that a later run would keep
			return out, fmt.Errorf("layout: %s: %w", f.Path, err)
		}
		// The umask may have dropped the execute bit of the build script.
		if mode&0o111 != 0 {
			if err := os.Chmod(full, mode); err != nil {
				return out, fmt.Errorf("layout: %s: %w", f.Path, err)
			}
		}
		out = append(out, FileResult{Path: f.Path, Created: true})
	}
	return out, nil
}
