package config

import (
	"context"
	"fmt"
	"strings"
)

type initTemplateData struct {
	BaseGo       bool
	Repo         string
	Pattern      string
	Filter       string
	PatternKnown bool
}

func RenderInitConfigTemplate(ctx context.Context, rootDir string) ([]byte, error) {
	data := initTemplateData{}

	repo, err := goCurrentRepoName(ctx, rootDir)
	if err == nil && strings.TrimSpace(repo) != "" {
		data.BaseGo = true
		data.Repo = repo
		data.Pattern, data.PatternKnown = derivePatternFromRepo(repo)
		if data.PatternKnown {
			pattern, parseErr := parseCellPattern(data.Pattern)
			if parseErr == nil && pattern != nil {
				data.Filter = pattern.Regex()
			}
		}
	}

	if data.BaseGo {
		return []byte(renderGoInitTemplate(data)), nil
	}
	return []byte(renderGenericInitTemplate()), nil
}

func renderGenericInitTemplate() string {
	return strings.TrimSpace(`
# c2j project config
#
# Repo values may be repo names, URLs, or file paths.
# Short names may contain letters, numbers, underscores, dashes, and periods.
# Use ${{ cell }} exactly once in pattern.
#
# Uncomment base: go to derive:
# - self.repo via: go list -m -f '{{.Path}}'
# - dependents via: go list -m -f '{{if not .Main}}{{.Path}}{{end}}' all
#
# base: go
#
# pattern: 'github.com/acme/boo-${{ cell }}'
#
# dependents:
#   - github.com/acme/boo-root
#   - github.com/acme/boo-monkey
#
# Or use the object form:
# dependents:
#   command: |
#     printf '%s\n' \
#       github.com/acme/boo-root \
#       github.com/acme/boo-monkey
#   filter: '^github\\.com/acme/boo-([A-Za-z0-9._-]+)$'
#
# self:
#   repo: github.com/acme/boo-self
#   # Optional override. By default tenant_id is derived from self.repo.
#   tenant_id: your-tenant-id
#   ref: main
#
# root:
#   repo: root
#   ref: main
`) + "\n"
}

func renderGoInitTemplate(data initTemplateData) string {
	var b strings.Builder
	b.WriteString("# c2j project config\n")
	b.WriteString("#\n")
	b.WriteString("# `base: go` derives:\n")
	b.WriteString("# - self.repo via: go list -m -f '{{.Path}}'\n")
	b.WriteString("# - dependents via: go list -m -f '{{if not .Main}}{{.Path}}{{end}}' all\n")
	b.WriteString("base: go\n\n")

	b.WriteString(fmt.Sprintf("# Derived current repo: %s\n", data.Repo))
	if data.PatternKnown {
		rootRepo := strings.Replace(data.Pattern, cellPlaceholder, "root", 1)
		b.WriteString(fmt.Sprintf("# Derived pattern: %s\n", data.Pattern))
		b.WriteString(fmt.Sprintf("# Derived root repo: %s\n", rootRepo))
		b.WriteString("#\n")
		b.WriteString("# Uncomment to override:\n")
		b.WriteString(fmt.Sprintf("# pattern: '%s'\n", data.Pattern))
		b.WriteString("# self:\n")
		if name, ok := shortNameFromRepoPattern(data.Repo, data.Pattern); ok {
			b.WriteString(fmt.Sprintf("#   repo: %s\n", name))
		} else {
			b.WriteString(fmt.Sprintf("#   repo: %s\n", data.Repo))
		}
		b.WriteString("#   ref: main\n")
		b.WriteString("#   # Optional override. By default tenant_id is derived from self.repo.\n")
		b.WriteString("#   tenant_id: your-tenant-id\n")
		b.WriteString("# root:\n")
		b.WriteString("#   repo: root\n")
		b.WriteString("#   ref: main\n")
		b.WriteString("# dependents:\n")
		b.WriteString(fmt.Sprintf("#   filter: '%s'\n", data.Filter))
		return b.String()
	}

	b.WriteString("# Pattern could not be inferred automatically.\n")
	b.WriteString("# Add `pattern` manually if you want short names.\n")
	b.WriteString("# `root.repo` must also be set explicitly before you can submit a root ticket.\n")
	b.WriteString("#\n")
	b.WriteString("# pattern: 'github.com/acme/boo-${{ cell }}'\n")
	b.WriteString("# self:\n")
	b.WriteString(fmt.Sprintf("#   repo: %s\n", data.Repo))
	b.WriteString("#   # Optional override. By default tenant_id is derived from self.repo.\n")
	b.WriteString("#   tenant_id: your-tenant-id\n")
	b.WriteString("#   ref: main\n")
	b.WriteString("# root:\n")
	b.WriteString("#   repo: github.com/acme/boo-root\n")
	b.WriteString("#   ref: main\n")
	return b.String()
}

func shortNameFromRepoPattern(repo string, pattern string) (string, bool) {
	cellPattern, err := parseCellPattern(pattern)
	if err != nil || cellPattern == nil {
		return "", false
	}
	return cellPattern.Match(repo)
}
