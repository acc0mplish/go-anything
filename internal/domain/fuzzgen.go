package domain

import (
	"math/rand/v2"
	"strings"
)

// fuzzPieces are the fragments inputs are built from. They cover what programs usually get wrong:
// empty and whitespace, quotes and escapes, markup, non-ASCII text, emoji and zero-width joiners,
// number edge cases and a NUL byte. Newlines are excluded because each input is one stdin line.
var fuzzPieces = []string{
	"a", "Z", "0", "9", " ", "  ", "\t", "-", "+", ".", ",", "_", "/", "\\",
	`"`, "'", "`", "<", ">", "&", ";", "|", "$", "%", "*", "{", "}", "[", "]",
	"é", "ü", "ß", "İ", "ı", "日本", "한글", "Ω", "\u00a0", "\u200d", "😀", "👍🏽",
	"-0", "NaN", "Infinity", "1e308", "9223372036854775807", "0x1F", "007",
	"٣", "१२", "²", "½", "١٢٣",
	"\x00", "\u202e",
}

// FuzzInputs returns n inputs, one per line, derived only from seed: the same seed always gives the
// same inputs, so a mismatch found in one run can be replayed by its seed.
// Most inputs are short; about one in twenty is long, to reach length-dependent code.
func FuzzInputs(seed uint64, n int) []string {
	r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		count := r.IntN(6)
		if r.IntN(20) == 0 {
			count = 200 + r.IntN(400)
		}
		var b strings.Builder
		for j := 0; j < count; j++ {
			b.WriteString(fuzzPieces[r.IntN(len(fuzzPieces))])
		}
		out = append(out, b.String())
	}
	return out
}
