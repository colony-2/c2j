// Package cellref resolves cell names using portable, captured project settings.
package cellref

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/colony-2/c2j/internal/repository"
)

const DefaultRef = "main"

var shortName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Context struct {
	Pattern  string `json:"pattern,omitempty"`
	SelfRepo string `json:"self_repo,omitempty"`
	SelfRef  string `json:"self_ref,omitempty"`
	RootRepo string `json:"root_repo,omitempty"`
	RootRef  string `json:"root_ref,omitempty"`
	BaseDir  string `json:"base_dir,omitempty"`
}

type Target struct{ Cell, Repository, Ref string }

func (c Context) Resolve(value, ref string) (Target, error) {
	value = strings.TrimSpace(value)
	ref = strings.TrimSpace(ref)
	if value == "" {
		return Target{}, fmt.Errorf("workspace.cell is required")
	}
	repo := value
	if value == "root" {
		repo = c.RootRepo
		if repo == "" {
			return Target{}, fmt.Errorf("root cell requires root.repo")
		}
	} else if shortName.MatchString(value) {
		if c.Pattern == "" {
			return Target{}, fmt.Errorf("cell %q requires a captured naming pattern or an explicit repository", value)
		}
		repo = strings.ReplaceAll(c.Pattern, "${{ cell }}", value)
	}
	if strings.HasPrefix(repo, "./") || strings.HasPrefix(repo, "../") {
		if c.BaseDir == "" {
			return Target{}, fmt.Errorf("relative repository requires a captured base directory")
		}
		repo = filepath.Join(c.BaseDir, repo)
	}
	normalized, err := repository.Normalize(repo)
	if err != nil {
		return Target{}, err
	}
	if ref == "" {
		if c.equal(normalized, c.RootRepo) {
			ref = c.RootRef
		} else if c.equal(normalized, c.SelfRepo) {
			ref = c.SelfRef
		}
		if ref == "" {
			ref = DefaultRef
		}
	}
	cell := value
	if !shortName.MatchString(cell) {
		cell = repository.Name(normalized)
		if c.Pattern != "" {
			pattern := strings.ReplaceAll(regexp.QuoteMeta(c.Pattern), regexp.QuoteMeta("${{ cell }}"), "([A-Za-z0-9._-]+)")
			if re, err := regexp.Compile("^" + pattern + "$"); err == nil {
				if match := re.FindStringSubmatch(repo); len(match) == 2 {
					cell = match[1]
				}
			}
		}
	}
	return Target{Cell: cell, Repository: normalized, Ref: ref}, nil
}
func (c Context) equal(a, b string) bool {
	if b == "" {
		return false
	}
	if strings.HasPrefix(b, "./") || strings.HasPrefix(b, "../") {
		if c.BaseDir == "" {
			return false
		}
		b = filepath.Join(c.BaseDir, b)
	}
	n, err := repository.Normalize(b)
	return err == nil && n == a
}
