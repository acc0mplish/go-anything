package analyze

import (
	"strconv"
	"strings"
)

// goKeywords cannot be package names.
var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true, "default": true,
	"defer": true, "else": true, "fallthrough": true, "for": true, "func": true, "go": true,
	"goto": true, "if": true, "import": true, "interface": true, "map": true, "package": true,
	"range": true, "return": true, "select": true, "struct": true, "switch": true, "type": true,
	"var": true,
}

// goIgnoredDirs are directory names the go tool skips or treats specially, so a package there
// would not build as an ordinary package. main would make a command.
var goIgnoredDirs = map[string]bool{"testdata": true, "vendor": true, "main": true}

// GoPackagePath suggests where one unit of a non-Go original lands in the Go rewrite. segments
// is the unit's path in the original's own notation, split into its parts: ["shop", "price"]
// for the Python module shop.price. The result is "internal/" followed by one Go package name
// per segment: lower case ASCII letters and digits only (so "_" and "-" are dropped, as Go
// package names do not use them), a leading digit prefixed with "p", a Go keyword or a name the
// go tool treats specially suffixed with "pkg", and an empty segment named "x". A leading
// "internal" segment is dropped, since the path already starts there; a deeper one stays and
// keeps its meaning in Go. A Python __main__ module is a command, so it lands in cmd/<its package>.
// It is pure: the same segments always give the same path.
func GoPackagePath(segments []string) string {
	if n := len(segments); n > 0 && segments[n-1] == "__main__" {
		name := "main"
		if n > 1 {
			name = goPackageName(segments[n-2])
		}
		return "cmd/" + name
	}
	parts := make([]string, 0, len(segments)+1)
	parts = append(parts, "internal")
	for i, s := range segments {
		if i == 0 && len(segments) > 1 && goPackageName(s) == "internal" {
			continue
		}
		parts = append(parts, goPackageName(s))
	}
	if len(parts) == 1 {
		parts = append(parts, "x")
	}
	return strings.Join(parts, "/")
}

// goPackageName turns one segment into a valid, conventional Go package name.
func goPackageName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	switch {
	case name == "":
		return "x"
	case name[0] >= '0' && name[0] <= '9':
		return "p" + name
	case goKeywords[name] || goIgnoredDirs[name]:
		return name + "pkg"
	}
	return name
}

// assignGoPackages fills GoPackage for every unit. When two units would land in the same Go
// package (shop/price.ts and shop/price/index.ts, or price_list and pricelist), the later one in
// import path order gets a numbered suffix, so the suggestion never merges units silently.
func assignGoPackages(units []*unitData, segments func(*unitData) []string) {
	used := map[string]bool{}
	for _, u := range units {
		p := GoPackagePath(segments(u))
		if used[p] {
			base := p
			for i := 2; used[p]; i++ {
				p = base + strconv.Itoa(i)
			}
		}
		used[p] = true
		u.pkg.GoPackage = p
	}
}
