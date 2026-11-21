package domain

import (
	"math/rand"
	"strings"
	"testing"
)

func TestProbeValuesAreDeterministicAndBounded(t *testing.T) {
	a := ProbeValues(5, 30)
	b := ProbeValues(5, 30)
	if len(a) != len(b) || len(a) > 30 {
		t.Fatalf("want at most 30 values, same for the same seed: %d %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("the same seed must give the same values")
		}
	}
	if a[0] != "" || !contains(a, "100") || !contains(a, "101") {
		t.Fatal("boundary values must be included, the empty value first")
	}
}

func TestProbeValuesNeverHoldNewlinesOrNUL(t *testing.T) {
	for _, v := range ProbeValues(9, 200) {
		if strings.ContainsAny(v, "\n\r\x00") {
			t.Fatalf("value %q cannot be a command-line argument", v)
		}
	}
}

// classExamples gives at least one value per class; each must map to exactly that class.
var classExamples = map[string][]string{
	classEmpty:         {""},
	classPadded:        {" 9 ", " 1", "7\t", " "},
	classSigned:        {"-1", "+5", "-0", "-", "-007"},
	classOverflow:      {"9999999999999999999999", "10000000000000000000", "00000000000000000000001"},
	classLeadingZero:   {"007", "00", "01", "0001"},
	classASCIIDigits:   {"0", "1", "10", "100", "9999999999999999999"},
	classUnicodeDigit:  {"٣", "१२", "۱۲۳", "０", "1٣"},
	classNumericSymbol: {"²", "³", "½", "Ⅻ", "1²"},
	classNonNumeric:    {"1x", "3 extra", "NaN", "1e3", "1,000", "abc", "\x00", "\xff", "\xd9"},
}

func TestProbeClassOfExamplesMapToTheirClass(t *testing.T) {
	if len(classExamples) != len(ProbeClasses()) {
		t.Fatalf("every class needs examples: %d examples for %d classes", len(classExamples), len(ProbeClasses()))
	}
	for class, values := range classExamples {
		for _, v := range values {
			if got := ProbeClassOf(v); got != class {
				t.Errorf("ProbeClassOf(%q) = %q, want %q", v, got, class)
			}
		}
	}
}

func TestProbeClassOfFirstMatchRule(t *testing.T) {
	cases := map[string]string{
		"007":                     classLeadingZero,   // leading-zero beats ascii-digits
		"0":                       classASCIIDigits,   // a single zero is not leading-zero
		"10":                      classASCIIDigits,   // a non-zero start is ascii-digits
		"-007":                    classSigned,        // signed beats leading-zero
		" 007":                    classPadded,        // padded beats leading-zero
		"00000000000000000000001": classOverflow,      // overflow beats leading-zero
		"٣ ":                      classPadded,        // padded beats unicode-digit
		"²1":                      classNumericSymbol, // a number that is not all decimal digits
	}
	for v, want := range cases {
		if got := ProbeClassOf(v); got != want {
			t.Errorf("ProbeClassOf(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestProbeClassOfIsTotal(t *testing.T) {
	hard := []string{"", "\x00", "\xff", "\xc3", "\xed\xa0\x80", "\x00 ", "\uFFFD", "-", "+", " ", "٣\xff"}
	allowed := map[string]bool{}
	for _, c := range ProbeClasses() {
		allowed[c] = true
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		b := make([]byte, rng.Intn(25))
		rng.Read(b)
		hard = append(hard, string(b))
	}
	for _, v := range hard {
		if c := ProbeClassOf(v); !allowed[c] {
			t.Fatalf("ProbeClassOf(%q) = %q, not a listed class", v, c)
		}
	}
	if ProbeClassOf("") != "empty" || ProbeClassOf("\x00") != "non-numeric" || ProbeClassOf("\xff") != "non-numeric" {
		t.Fatal("empty, NUL and invalid UTF-8 must map to empty and non-numeric")
	}
}

func TestProbeClassesFixedOrderAndCopy(t *testing.T) {
	want := []string{"empty", "padded", "signed", "overflow", "leading-zero", "ascii-digits", "unicode-digit", "numeric-symbol", "non-numeric"}
	got := ProbeClasses()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("classes out of order: %v", got)
	}
	got[0] = "changed"
	if ProbeClasses()[0] != "empty" {
		t.Fatal("ProbeClasses must return a copy")
	}
}

func TestProbeBoundaryValuesCoverEveryClass(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range probeBoundaries {
		seen[ProbeClassOf(v)] = true
	}
	for _, c := range ProbeClasses() {
		if !seen[c] {
			t.Errorf("boundary values have no %q value", c)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestUnicodeLeadingZeroIsLeadingZero(t *testing.T) {
	cases := map[string]string{
		"٠١": classLeadingZero,  // Arabic-Indic zero then one
		"０１": classLeadingZero,  // fullwidth zero then one
		"𝟎𝟏": classLeadingZero,  // mathematical bold zero then one
		"١٢": classUnicodeDigit, // Arabic-Indic one then two: no leading zero
		"१२": classUnicodeDigit, // Devanagari one then two
		"٠":  classUnicodeDigit, // a single zero is not a leading zero
		"٣":  classUnicodeDigit,
	}
	for v, want := range cases {
		if got := ProbeClassOf(v); got != want {
			t.Fatalf("ProbeClassOf(%q) = %q, want %q", v, got, want)
		}
	}
}
