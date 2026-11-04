package execution

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func validateBase(value string, concrete bool) error {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00 \t\r\n") {
		return fmt.Errorf("base must be nonempty UTF-8, at most 4096 bytes, without NUL, space, tab, CR, or LF")
	}
	if concrete && strings.Contains(value, "*") {
		return fmt.Errorf("actual base must be a concrete name without '*'")
	}
	return nil
}

// MatchBase matches an entire concrete name, case-sensitively. Only '*' is
// special; it matches zero or more characters, including '/'. A selector of
// '*' is unconstrained, including when the allocation reports no base.
// Callers accepting external values must validate them before matching.
func MatchBase(selector, actual string) bool {
	if selector == "*" {
		return true
	}
	if actual == "" {
		return false
	}
	parts := strings.Split(selector, "*")
	if len(parts) == 1 {
		return selector == actual
	}
	if !strings.HasPrefix(actual, parts[0]) {
		return false
	}
	actual = actual[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(actual, part)
		if i < 0 {
			return false
		}
		actual = actual[i+len(part):]
	}
	return strings.HasSuffix(actual, parts[len(parts)-1])
}
