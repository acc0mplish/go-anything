// Package usecase defines application behavior. It depends only on domain and on the
// port interfaces declared here; adapters implement those ports from the outside.
package usecase

import "github.com/go-anything/go-anything/internal/domain"

// RecordingStore persists and loads recordings. Implemented by adapters.
type RecordingStore interface {
	Load(path string) (domain.Recording, error)
	Save(path string, rec domain.Recording) error
}

// Subject is an implementation that maps an input to an output (or an error).
// Both the original and the rewrite are Subjects.
type Subject interface {
	Run(input string) (string, error)
}
