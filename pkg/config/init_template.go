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

	repo, err := goCurrentRepoName(rootDir)
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
		return []byte(renderGoInitTemplate(data) + packageTemplate), nil
	}
	return []byte(renderGenericInitTemplate() + packageTemplate), nil
}

const packageTemplate = `
# Packages available to every live operation in this repository.
# Use pinned registry versions or Nix revisions for repeatable resolution.
# Nix supports executables and native libraries; pnpm/uv install CLI applications.
# packages:
#   - nix:nixpkgs#go
#   - nix:nixpkgs#openssl
#   - pnpm:playwright@1.51.1
#   - uv:ruff==0.11.2
`

func renderGenericInitTemplate() string {
	return strings.TrimSpace(`
# c2j project config
#
# Repo values may be repo names, URLs, or file paths.
# Short names may contain letters, numbers, underscores, dashes, and periods.
# Use ${{ cell }} exactly once in pattern.
#
# Uncomment base: go to derive:
# - self.repo from the module directive in go.mod
# - dependents from direct require entries (excluding // indirect) in go.mod
# No Go installation is needed. Use base: gocmd for go list -m all discovery,
# including transitive dependencies; gocmd requires Go to be installed.
#
# base: go
#
# jobdb: https://jobdb.example.com/your-tenant-id
#
# pattern: 'github.com/acme/boo-${{ cell }}'
#
# dependents:
#   - github.com/acme/boo-root
#   - repo: github.com/acme/boo-monkey
#     description: Monkey service
#
# Strings and mappings are the simple and detailed forms of a manual entry.
# A manual list replaces discovery. Detected cells can be used directly.
# Edit manual lists with: c2j cell add <repo> --description 'Service description'
# Add --stdout to preview the proposed config without writing.
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
	b.WriteString("# - self.repo from the module directive in go.mod\n")
	b.WriteString("# - dependents from direct require entries (excluding // indirect) in go.mod\n")
	b.WriteString("# No Go installation is needed. Use base: gocmd for go list -m all discovery,\n")
	b.WriteString("# including transitive dependencies; gocmd requires Go to be installed.\n")
	b.WriteString("base: go\n\n")
	b.WriteString("# Detected dependent cells are used directly; inspect them with c2j cells.\n")
	b.WriteString("# To maintain an explicit list instead, uncomment and edit:\n")
	b.WriteString("# dependents:\n")
	b.WriteString("#   - github.com/acme/shared\n")
	b.WriteString("#   - repo: github.com/acme/auth\n")
	b.WriteString("#     description: Authentication service\n")
	b.WriteString("# Edit manual lists with: c2j cell add <repo> --description 'Service description'\n")
	b.WriteString("# Add --stdout to preview without writing.\n\n")
	b.WriteString("# JobDB URI: https://<jobdb-host>/<tenant-id> or embed:///\n")
	b.WriteString("# jobdb: https://jobdb.example.com/your-tenant-id\n\n")

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
