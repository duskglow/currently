package nutproto

import (
	"errors"
	"strings"
)

var errUnterminated = errors.New("unterminated quote")

// split tokenizes a protocol line: words separated by spaces or tabs, or
// double-quoted strings in which \" and \\ are escapes.
func split(line string) ([]string, error) {
	var (
		args   []string
		cur    strings.Builder
		inWord bool
		quoted bool
		escape bool
	)
	for _, r := range line {
		switch {
		case escape:
			cur.WriteRune(r)
			escape = false
		case r == '\\' && quoted:
			escape = true
		case r == '"':
			if quoted {
				args = append(args, cur.String())
				cur.Reset()
				quoted, inWord = false, false
			} else {
				if inWord {
					args = append(args, cur.String())
					cur.Reset()
				}
				quoted, inWord = true, false
			}
		case quoted:
			cur.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\r':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quoted {
		return nil, errUnterminated
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}

// quote renders a value as a protocol string.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
