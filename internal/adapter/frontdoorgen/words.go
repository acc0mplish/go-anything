package frontdoorgen

import (
	"errors"
	"fmt"
	"strings"
)

// ErrCommand is returned when -original-cmd cannot be split into a program and its arguments.
var ErrCommand = errors.New("frontdoorgen: original command")

// SplitWords splits a command line the way a POSIX shell splits words, and nothing more: single quotes
// keep everything literally, double quotes keep spaces (a backslash escapes " \ $ and ` inside them),
// and a backslash outside quotes escapes the next character. The command never runs through a shell,
// so anything a shell would do beyond splitting (pipes, redirections, variables, command substitution,
// a leading ~) is refused instead of being passed on as a surprising literal. Quote it to mean it literally.
func SplitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 0:
			return nil, fmt.Errorf("%w: contains a NUL byte", ErrCommand)
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("%w: unbalanced single quote at offset %d", ErrCommand, i)
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '"':
			start := i
			closed := false
			for i++; i < len(s); i++ {
				d := s[i]
				if d == '"' {
					closed = true
					break
				}
				if d == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
					cur.WriteByte(s[i])
					continue
				}
				if d == '$' || d == '`' {
					return nil, fmt.Errorf("%w: %q inside double quotes would be expanded by a shell; no shell is used, so use single quotes or escape it", ErrCommand, string(d))
				}
				if d == 0 {
					return nil, fmt.Errorf("%w: contains a NUL byte", ErrCommand)
				}
				cur.WriteByte(d)
			}
			if !closed {
				return nil, fmt.Errorf("%w: unbalanced double quote at offset %d", ErrCommand, start)
			}
			inWord = true
		case c == '\\':
			if i+1 >= len(s) {
				return nil, fmt.Errorf("%w: ends with a lone backslash", ErrCommand)
			}
			i++
			cur.WriteByte(s[i])
			inWord = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.IndexByte("|&;<>()$`", c) >= 0:
			return nil, fmt.Errorf("%w: unquoted %q at offset %d; the command runs without a shell, so pipes, redirections and expansions do not work (quote it if it is a literal argument)", ErrCommand, string(c), i)
		case c == '~' && !inWord:
			return nil, fmt.Errorf("%w: a leading ~ is not expanded without a shell; write the full path", ErrCommand)
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrCommand)
	}
	return words, nil
}
