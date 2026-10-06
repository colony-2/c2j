package common

import (
	"fmt"
	"path"
	"strings"
)

const DefaultGitAuthor = "c2j <c2j@colony2>"

// GitAuthorForCell supplies the fallback identity when no author is configured.
// The root cell is a path marker, not a person: Git rejects "." as an author name.
func GitAuthorForCell(cell string) string {
	cell = strings.TrimSpace(cell)
	if cell == "" || path.Clean(cell) == "." {
		return DefaultGitAuthor
	}
	return fmt.Sprintf("%s <%s@colony2>", cell, cell)
}
