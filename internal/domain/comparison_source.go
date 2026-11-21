package domain

import (
	"embed"
)

// comparisonFiles are the sources of the answer comparison. They use only the standard library, so they
// compile inside any Go module.
//
//go:embed compare.go answer.go
var comparisonFiles embed.FS

// ComparisonSources returns the source text of the answer comparison by file name. Generated unit tests
// are built from these files, so a generated test and the tool judge an answer by the same code.
func ComparisonSources() (map[string]string, error) {
	out := map[string]string{}
	for _, name := range []string{"compare.go", "answer.go"} {
		b, err := comparisonFiles.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out[name] = string(b)
	}
	return out, nil
}
