package repository

import (
	"net/url"
	"path"
	"strings"
)

func Name(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if strings.HasPrefix(source, "git@") {
		if i := strings.Index(source, ":"); i >= 0 {
			source = source[i+1:]
		}
	}
	if u, err := url.Parse(source); err == nil && u.Scheme != "" {
		source = u.Path
	}
	return strings.TrimSuffix(path.Base(strings.TrimSuffix(source, "/")), ".git")
}
