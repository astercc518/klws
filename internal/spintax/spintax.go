// Package spintax expands "spintax" templates: {a|b|c} picks one option at
// random. Groups may nest: {a {b|c}|d}. Used to vary bulk-send copy per
// recipient (anti-ban). Unmatched '{' or '}' are treated as literal text.
package spintax

import (
	"math/rand"
	"strings"
)

// Expand walks the string once, recursively resolving each {…|…} group with r.
func Expand(s string, r *rand.Rand) string {
	out, _ := expand(s, 0, r)
	return out
}

// expand parses from index i until end-of-string or an unmatched '}'.
// It returns the rendered text and the index just past where it stopped.
// Double-braces {{ and }} are passed through literally so that Go template
// syntax ({{.var}}) is left intact for the subsequent renderTemplate pass.
func expand(s string, i int, r *rand.Rand) (string, int) {
	var b strings.Builder
	for i < len(s) {
		switch s[i] {
		case '{':
			// {{ is a Go template delimiter — pass through literally.
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteString("{{")
				i += 2
				continue
			}
			choice, next := expandGroup(s, i+1, r)
			b.WriteString(choice)
			i = next
		case '}':
			// }} is a Go template delimiter — pass through literally.
			if i+1 < len(s) && s[i+1] == '}' {
				b.WriteString("}}")
				i += 2
				continue
			}
			return b.String(), i + 1 // end of the enclosing group
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), i
}

// expandGroup parses options separated by '|' starting just after '{', picks
// one uniformly, and returns it plus the index just past the closing '}'.
func expandGroup(s string, i int, r *rand.Rand) (string, int) {
	var options []string
	var cur strings.Builder
	for i < len(s) {
		switch s[i] {
		case '{':
			sub, next := expandGroup(s, i+1, r)
			cur.WriteString(sub)
			i = next
		case '|':
			options = append(options, cur.String())
			cur.Reset()
			i++
		case '}':
			options = append(options, cur.String())
			i++
			if len(options) == 0 {
				return "", i
			}
			return options[r.Intn(len(options))], i
		default:
			cur.WriteByte(s[i])
			i++
		}
	}
	// Unmatched '{': treat the whole thing literally (best-effort).
	return "{" + cur.String(), i
}
