// Package cellref separates cell names from repository assertions.
package cellref

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

const DefaultRef = "main"

type Context struct {
	Pattern  string `json:"pattern,omitempty"`
	SelfCell string `json:"self_cell,omitempty"`
	SelfRepo string `json:"self_repo,omitempty"`
	SelfRef  string `json:"self_ref,omitempty"`
	RootRepo string `json:"root_repo,omitempty"`
	RootRef  string `json:"root_ref,omitempty"`
}

type Target struct{ Cell, Repository, Ref string }
type Resolver func(context.Context, string) (jobdb.ResolvedCell, error)

// Assertion captures routing configuration, never ownership or registration.
func (c Context) Assertion(name string) string {
	switch {
	case name == "root":
		return c.RootRepo
	case name == c.SelfCell:
		return c.SelfRepo
	case c.Pattern != "":
		return strings.ReplaceAll(c.Pattern, "${{ cell }}", name)
	default:
		return ""
	}
}

func (c Context) Resolve(value, ref string) (Target, error) {
	if err := jobdb.ValidateCellName(value); err != nil {
		return Target{}, fmt.Errorf("cell must be a name: %w", err)
	}
	repo := c.Assertion(value)
	if repo != "" {
		var err error
		repo, err = jobdb.NormalizeCellRepo(repo)
		if err != nil {
			return Target{}, fmt.Errorf("cell repository: %w", err)
		}
	}
	if ref == "" {
		if value == "root" {
			ref = c.RootRef
		} else if value == c.SelfCell {
			ref = c.SelfRef
		}
		if ref == "" {
			ref = DefaultRef
		}
	}
	return Target{Cell: value, Repository: repo, Ref: ref}, nil
}

// ResolveTarget retains the registry repo as an assertion through submission.
// Missing ordinary cells are registered only by the eventual SubmitJob transaction.
func ResolveTarget(ctx context.Context, resolve Resolver, name, repo string) (jobdb.ResolvedCell, error) {
	if err := jobdb.ValidateCellName(name); err != nil {
		return jobdb.ResolvedCell{}, fmt.Errorf("cell must be a name: %w", err)
	}
	if repo != "" {
		var err error
		repo, err = jobdb.NormalizeCellRepo(repo)
		if err != nil {
			return jobdb.ResolvedCell{}, fmt.Errorf("cell repository: %w", err)
		}
	}
	if resolve == nil {
		return jobdb.ResolvedCell{}, fmt.Errorf("cell registry resolver is required")
	}
	cell, err := resolve(ctx, name)
	if err != nil {
		if errors.Is(err, jobdb.ErrCellNotFound) && repo != "" && name != "root" {
			return jobdb.ResolvedCell{Cell: name, CellRepo: repo}, nil
		}
		return jobdb.ResolvedCell{}, err
	}
	if repo != "" && repo != cell.CellRepo {
		return jobdb.ResolvedCell{}, fmt.Errorf("cell %q configured repo %q differs from registered repo %q: %w", name, repo, cell.CellRepo, jobdb.ErrCellRepoMismatch)
	}
	return cell, nil
}

func Registry(runtime jobdb.CellRuntime, tenant string) Resolver {
	return func(ctx context.Context, name string) (jobdb.ResolvedCell, error) {
		page, err := runtime.ListCells(ctx, jobdb.ListCellsRequest{TenantID: tenant, Name: name, State: "ALL", PageSize: 1})
		if err != nil {
			return jobdb.ResolvedCell{}, err
		}
		if len(page.Cells) == 0 {
			return jobdb.ResolvedCell{}, jobdb.ErrCellNotFound
		}
		if page.Cells[0].State != jobdb.CellActive {
			return jobdb.ResolvedCell{}, jobdb.ErrCellRetired
		}
		return page.Cells[0].Resolved(), nil
	}
}
