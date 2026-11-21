package analyze

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// Languages the analyzer accepts for Source.Lang.
const (
	LangAuto   = "auto"
	LangGo     = "go"
	LangPython = "python"
	LangJS     = "js"
	LangC      = "c"
	LangJava   = "java"
)

// SupportedLanguages is the list shown when a language is unknown or cannot be detected.
const SupportedLanguages = "go (a go.mod at the root), python (.py), js (.js, .mjs, .cjs, .jsx, .ts, .tsx, .mts, .cts), c (.c with .h headers), java (.java under src/main/java)"

// skipDirs are never walked for a non-Go original: dependencies, virtual environments, caches and
// build output are not the program's own source.
var skipDirs = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true, "dist": true,
	"build": true, ".git": true,
}

// skipDir reports whether a directory below the root is left out of a non-Go analysis. Hidden
// directories are left out too: they hold tool state such as .go-anything, .tox or .mypy_cache.
func skipDir(name string) bool {
	return skipDirs[name] || strings.HasPrefix(name, ".")
}

// jsExts are the JavaScript and TypeScript source extensions.
var jsExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".jsx": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true,
}

// isJSSource reports whether name is a JS or TS source file. Type declaration files (.d.ts) hold
// no code and are left out.
func isJSSource(name string) bool {
	if strings.HasSuffix(name, ".d.ts") || strings.HasSuffix(name, ".d.mts") || strings.HasSuffix(name, ".d.cts") {
		return false
	}
	return jsExts[filepath.Ext(name)]
}

// Source analyzes a tree in the language Lang names, or the detected one for "" and "auto".
// Python is the interpreter used to parse Python sources (default python3).
type Source struct {
	Lang   string
	Python string
}

// Analyze dispatches to the analyzer of the language.
func (s Source) Analyze(root string) (domain.Analysis, error) {
	lang := s.Lang
	if lang == "" || lang == LangAuto {
		var err error
		if lang, err = DetectLanguage(root); err != nil {
			return domain.Analysis{}, err
		}
	}
	switch lang {
	case LangGo:
		return GoSource{}.Analyze(root)
	case LangPython:
		return PythonSource{Python: s.Python}.Analyze(root)
	case LangJS:
		return JSSource{}.Analyze(root)
	case LangC:
		return CSource{}.Analyze(root)
	case LangJava:
		return JavaSource{}.Analyze(root)
	}
	return domain.Analysis{}, fmt.Errorf("analyze: unknown language %q; use auto, go, python or js; c and java are also supported", s.Lang)
}

// DetectLanguage picks the language of a tree: go when the root has a go.mod, else python, js,
// c or java, whichever has more source files outside dependency, environment and build folders.
// A tie or an empty tree is an error that names the supported languages, since guessing would be
// wrong.
func DetectLanguage(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("analyze: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("analyze: %s is not a directory", root)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		return LangGo, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("analyze: %w", err)
	}
	py, js, c, java, err := countSources(root)
	if err != nil {
		return "", err
	}
	counts := []struct {
		n     int
		label string
		lang  string
	}{
		{py, "Python", LangPython},
		{js, "JS/TS", LangJS},
		{c, "C", LangC},
		{java, "Java", LangJava},
	}
	max := 0
	for _, ct := range counts {
		if ct.n > max {
			max = ct.n
		}
	}
	switch {
	case max == 0:
		return "", fmt.Errorf("analyze: no supported source under %s; supported: %s", root, SupportedLanguages)
	case counts[0].n == max && counts[1].n < max && counts[2].n < max && counts[3].n < max:
		return LangPython, nil
	case counts[1].n == max && counts[0].n < max && counts[2].n < max && counts[3].n < max:
		return LangJS, nil
	case counts[2].n == max && counts[0].n < max && counts[1].n < max && counts[3].n < max:
		return LangC, nil
	case counts[3].n == max && counts[0].n < max && counts[1].n < max && counts[2].n < max:
		return LangJava, nil
	}
	var tied, flags []string
	for _, ct := range counts {
		if ct.n == max {
			tied = append(tied, ct.label)
			flags = append(flags, "-lang "+ct.lang)
		}
	}
	if len(tied) == 2 {
		return "", fmt.Errorf("analyze: %s has as many %s as %s files (%d each); choose one with %s or %s",
			root, tied[0], tied[1], max, flags[0], flags[1])
	}
	return "", fmt.Errorf("analyze: %s has %d files each of %s; choose one with %s",
		root, max, strings.Join(tied, ", "), strings.Join(flags, " or "))
}

// countSources counts Python, JS/TS, C and Java source files, skipping the folders skipDir
// names.
func countSources(root string) (py, js, c, java int, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			return nil // an unreadable folder below the root does not decide the language
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		switch {
		case strings.HasSuffix(d.Name(), ".py"):
			py++
		case isJSSource(d.Name()):
			js++
		case strings.HasSuffix(d.Name(), ".c"):
			c++
		case strings.HasSuffix(d.Name(), ".java"):
			java++
		}
		return nil
	})
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("analyze: %w", err)
	}
	return py, js, c, java, nil
}
