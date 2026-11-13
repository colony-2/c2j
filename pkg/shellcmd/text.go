package shellcmd

import "strings"

// QuotePOSIX returns one literal POSIX shell argument. Shell arguments cannot
// contain NUL. Runtime expressions such as "$@" must remain shell source.
func QuotePOSIX(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "'\"'\"'") + "'"
}

// Literal emits text with no added newline. The data is separate from printf's
// fixed format. Callers may append redirections or shell control flow.
func Literal(text string) string { return "printf '%s' " + QuotePOSIX(text) }

// LiteralLine emits text followed by exactly one additional newline.
func LiteralLine(text string) string { return "printf '%s\\n' " + QuotePOSIX(text) }
