package usecase

import (
	"strings"
	"testing"
)

// TestComparisonCopyKeepsFieldAndSelectorNames guards the prefixer against the case where a struct
// field is named like a top-level function: the Normalize field meets the Normalize function in the
// embedded sources. Field names, literal keys and selector names must stay unprefixed, or the
// generated cases_test.go (which writes Normalize: [...]) stops compiling against the copy.
func TestComparisonCopyKeepsFieldAndSelectorNames(t *testing.T) {
	files, err := ComparisonTestFiles("orders")
	if err != nil {
		t.Fatal(err)
	}
	compare := string(files["ga_compare_test.go"])
	if !strings.Contains(compare, "Normalize []string `json:") {
		t.Fatal("the copy must keep the Normalize field declaration so the generated literal compiles")
	}
	if !strings.Contains(compare, "gaParseNormalizers(o.Normalize)") {
		t.Fatal("the copy must rename the parseNormalizers function and the opt.Normalize field access consistently")
	}
	if strings.Contains(compare, "gaNormalize []string") {
		t.Fatal("the copy must not rename the Normalize field itself")
	}
}
