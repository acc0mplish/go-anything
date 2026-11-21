package usecase

import "github.com/go-anything/go-anything/internal/domain"

// Analyzer inspects a source tree and reports what is there. Implemented by adapters.
type Analyzer interface {
	Analyze(root string) (domain.Analysis, error)
}

// Analyze returns the structure of a Go source tree, so a rewrite can be planned unit by unit.
func Analyze(a Analyzer, root string) (domain.Analysis, error) {
	return a.Analyze(root)
}
