package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Input class names. Every probe value belongs to exactly one class (see ProbeClassOf).
const (
	classEmpty         = "empty"
	classPadded        = "padded"
	classSigned        = "signed"
	classOverflow      = "overflow"
	classLeadingZero   = "leading-zero"
	classASCIIDigits   = "ascii-digits"
	classUnicodeDigit  = "unicode-digit"
	classNumericSymbol = "numeric-symbol"
	classNonNumeric    = "non-numeric"
)

// probeClassList is the fixed list of classes, in the order ProbeClassOf tests its rules.
var probeClassList = []string{
	classEmpty,
	classPadded,
	classSigned,
	classOverflow,
	classLeadingZero,
	classASCIIDigits,
	classUnicodeDigit,
	classNumericSymbol,
	classNonNumeric,
}

// ProbeClasses returns the names of all input classes in fixed order. The order is the order of the
// rules in ProbeClassOf: a value is assigned to the first class whose rule matches it.
// The returned slice is a copy and may be changed by the caller.
func ProbeClasses() []string {
	return append([]string(nil), probeClassList...)
}

// ProbeClassOf returns the single input class of v. It is total: every string, including the empty
// string, NUL, and invalid UTF-8, gets a class from ProbeClasses. The rules are tried in this order and
// the first match wins:
//
//  1. empty: the empty string.
//  2. padded: leading or trailing whitespace (so " 9 " is padded, not non-numeric).
//  3. signed: a leading '+' or '-' (so "-007" is signed, not leading-zero).
//  4. overflow: more than 19 ASCII digits only (so a long zero-padded value is overflow).
//  5. leading-zero: digits only, longer than one digit, starting with a zero digit of any script
//     ("007", "00", "٠١", "０１").
//  6. ascii-digits: ASCII digits only ("0", "1", "100").
//  7. unicode-digit: every rune is a Unicode decimal digit (unicode.IsDigit), with at least one
//     non-ASCII digit, since ASCII-only strings matched rules 4 to 6 ("٣", "१२", "０").
//  8. numeric-symbol: every rune is a Unicode number (unicode.IsNumber) but not all are decimal
//     digits, e.g. superscripts, fractions and roman numerals ("²", "½", "1²").
//  9. non-numeric: anything else ("1x", "NaN", "1e3", "\x00", invalid UTF-8).
func ProbeClassOf(v string) string {
	switch {
	case v == "":
		return classEmpty
	case strings.TrimSpace(v) != v:
		return classPadded
	case v[0] == '+' || v[0] == '-':
		return classSigned
	case isASCIIDigits(v) && len(v) > 19:
		return classOverflow
	case isASCIIDigits(v) && len(v) > 1 && v[0] == '0':
		return classLeadingZero
	case isASCIIDigits(v):
		return classASCIIDigits
	case allRunes(v, unicode.IsDigit) && utf8.RuneCountInString(v) > 1 && isZeroDigit(firstRune(v)):
		return classLeadingZero
	case allRunes(v, unicode.IsDigit):
		return classUnicodeDigit
	case allRunes(v, unicode.IsNumber):
		return classNumericSymbol
	default:
		return classNonNumeric
	}
}

// isASCIIDigits reports whether v is a non-empty string of bytes '0' to '9'.
func isASCIIDigits(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

// allRunes reports whether v is non-empty and every rune of v satisfies f. Invalid UTF-8 decodes to
// U+FFFD, which no numeric predicate accepts, so invalid bytes never count as numeric.
func allRunes(v string, f func(rune) bool) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if !f(r) {
			return false
		}
	}
	return true
}

// isZeroDigit reports whether r is the zero of its digit block. Unicode decimal digits come in runs of
// ten that start at zero, so a digit whose offset from the start of its Nd range is a multiple of ten is
// a zero. This holds for every Nd range in the Unicode tables.
func isZeroDigit(r rune) bool {
	if !unicode.IsDigit(r) {
		return false
	}
	for _, rg := range unicode.Nd.R16 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) {
			return (r-rune(rg.Lo))%10 == 0
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if rune(rg.Lo) <= r && r <= rune(rg.Hi) {
			return (r-rune(rg.Lo))%10 == 0
		}
	}
	return false
}

// firstRune returns the first rune of a non-empty string.
func firstRune(v string) rune {
	r, _ := utf8.DecodeRuneInString(v)
	return r
}
