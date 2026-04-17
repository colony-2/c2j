package recipe

import "strings"

func isSelectorOp(op string) bool {
	op = strings.TrimSpace(op)
	return strings.HasPrefix(op, "git+") || strings.HasPrefix(op, "./") || strings.HasPrefix(op, "../")
}
