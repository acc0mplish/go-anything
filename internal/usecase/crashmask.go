package usecase

import (
	"regexp"
	"strings"

	"github.com/go-anything/go-anything/internal/domain"
)

// CrashTextLimit is the most bytes of crash text (a traceback, a panic, a stack) written to disk.
const CrashTextLimit = domain.ReportValueLimit

var (
	// homeUnix matches a home directory: /home/<user>, /Users/<user> or /root.
	homeUnix = regexp.MustCompile(`/home/[^\s/'"():,;<>|]+|/Users/[^\s/'"():,;<>|]+|/root`)
	// homeWindows is a whole match of pathWindows that is only a home directory, C:\Users\<user>.
	homeWindows = regexp.MustCompile(`^[A-Za-z]:\\Users\\[^\\]+$`)
	// pathUnix matches an absolute or home-relative path. Separators, quotes, parentheses and colons end
	// a path, so "main.go:12" keeps its line number and `File "x.py", line 3` keeps its quote.
	pathUnix = regexp.MustCompile(`~?(?:/[^\s/'"():,;<>|\[\]{}]+)+`)
	// pathWindows matches C:\dir\file.
	pathWindows = regexp.MustCompile(`[A-Za-z]:(?:\\[^\s\\'"():,;<>|]+)+`)
	// exceptionHead matches a line that names an exception, as Node, Java and others print it.
	exceptionHead = regexp.MustCompile(`^[A-Za-z_][\w.$]*(?:Error|Exception)\b`)
)

// MaskCrashText prepares crash text for disk. Home directories become ~, absolute paths become their
// base file name (line numbers stay), and the text is clipped to CrashTextLimit bytes with
// domain.ClipForReport. The line that names the exception is kept whole even when clipping would cut it
// (a Python traceback prints it last), so the defect stays recognizable. The result can exceed the
// limit only by the clip marker. The function is pure: it reads no environment.
func MaskCrashText(s string) string {
	masked := maskPaths(s)
	if len(masked) <= CrashTextLimit {
		return masked
	}
	exc := ExceptionLine(masked)
	at := strings.Index(masked, exc)
	if exc == "" || at+len(exc) <= CrashTextLimit {
		return domain.ClipForReport(masked, CrashTextLimit)
	}
	budget := CrashTextLimit - len(exc) - 1
	if budget <= 0 {
		return domain.ClipForReport(exc, CrashTextLimit)
	}
	head := masked[:at]
	return strings.TrimRight(domain.ClipForReport(head, budget), "\n") + "\n" + exc
}

// maskPaths replaces home directories with ~ and absolute paths with their base name.
func maskPaths(s string) string {
	s = pathWindows.ReplaceAllStringFunc(s, func(m string) string {
		if homeWindows.MatchString(m) {
			return "~"
		}
		return m[strings.LastIndex(m, `\`)+1:]
	})
	s = replaceAt(s, homeUnix, func(string) string { return "~" })
	return replaceAt(s, pathUnix, func(m string) string {
		// A path needs two segments ("/etc" alone says nothing about the machine) unless it starts at ~.
		if !strings.HasPrefix(m, "~") && strings.Count(m, "/") < 2 {
			return m
		}
		return m[strings.LastIndex(m, "/")+1:]
	})
}

// pathChars are the characters a path segment or a URL is made of. The trailing "/" is a separator.
const pathChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-~\\/"

// replaceAt replaces the matches of re that start a path: at the start of the text or after a character
// that cannot be part of a path or URL. A match inside a URL ("http://host/a/b") or a relative path
// ("a/b/c") is left alone.
func replaceAt(s string, re *regexp.Regexp, repl func(string) string) string {
	var b strings.Builder
	prev := 0
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if loc[0] > 0 && strings.ContainsRune(pathChars+":", rune(s[loc[0]-1])) {
			continue
		}
		// The match must also end where a path segment ends: "/rootfs" is not the home directory "/root".
		if loc[1] < len(s) && strings.ContainsRune(pathChars[:len(pathChars)-1], rune(s[loc[1]])) {
			continue
		}
		b.WriteString(s[prev:loc[0]])
		b.WriteString(repl(s[loc[0]:loc[1]]))
		prev = loc[1]
	}
	b.WriteString(s[prev:])
	return b.String()
}

// ExceptionLine returns the line of crash text that names the failure: the "panic:" line of a Go panic,
// the last unindented line of a Python traceback, or the first "...Error"/"...Exception" line of a Node or
// Java stack. When none is found it returns the last non-blank line. Empty text returns "".
func ExceptionLine(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "panic: ") {
			return l
		}
	}
	if strings.Contains(s, "Traceback (most recent call last)") {
		for i := len(lines) - 1; i >= 0; i-- {
			l := lines[i]
			if strings.TrimSpace(l) != "" && l[0] != ' ' && l[0] != '\t' {
				return l
			}
		}
	}
	for _, l := range lines {
		if exceptionHead.MatchString(l) {
			return l
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
