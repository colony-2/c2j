package recipejob

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/pkg/cellref"
	configpkg "github.com/colony-2/c2j/pkg/config"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type TargetSource string

const (
	TargetSourceSelf   TargetSource = "self"
	TargetSourceConfig TargetSource = "config"
)

type ResolveTargetRequest struct {
	WorkingDir  string
	Cell        string
	CellRepo    string
	Self        bool
	TenantID    string
	ResolveCell cellref.Resolver
}
type ResolvedTarget struct {
	CellResolution   *cellref.Context `json:"cell_resolution,omitempty"`
	OriginalInput    string           `json:"original_input,omitempty"`
	ResolvedRepo     string           `json:"resolved_repo,omitempty"`
	RepositorySource string           `json:"repository_source"`
	CellRepo         string           `json:"cell_repo,omitempty"`
	CellID           string           `json:"cell_id,omitempty"`
	DefaultRef       string           `json:"default_ref"`
	CellName         string           `json:"cell_name"`
	TenantID         string           `json:"tenant_id,omitempty"`
	Source           TargetSource     `json:"source"`
}

func ResolveTarget(ctx context.Context, req ResolveTargetRequest) (ResolvedTarget, error) {
	if req.Self && req.Cell != "" {
		return ResolvedTarget{}, fmt.Errorf("self and cell are mutually exclusive")
	}
	dir, err := cleanWorkingDir(req.WorkingDir)
	if err != nil {
		return ResolvedTarget{}, err
	}
	cfg, err := configpkg.LoadProjectConfig(dir)
	if err != nil && err != configpkg.ErrConfigNotFound {
		return ResolvedTarget{}, err
	}
	captured := &cellref.Context{}
	if cfg != nil {
		captured, err = cfg.CellResolution(ctx)
		if err != nil {
			return ResolvedTarget{}, err
		}
	}
	name := strings.TrimSpace(req.Cell)
	source := TargetSourceConfig
	if name == "" {
		source = TargetSourceSelf
		if cfg != nil {
			name = cfg.SelfCell()
		}
		if name == "" {
			return ResolvedTarget{}, fmt.Errorf("current cell requires explicit self.cell in .c2/config.yaml")
		}
	}
	target, err := captured.Resolve(name, "")
	if err != nil {
		return ResolvedTarget{}, err
	}
	if req.CellRepo != "" {
		target.Repository, err = jobdb.NormalizeCellRepo(req.CellRepo)
		if err != nil {
			return ResolvedTarget{}, err
		}
	}
	out := ResolvedTarget{CellResolution: captured, OriginalInput: req.Cell, CellName: name, CellRepo: target.Repository, RepositorySource: target.Repository, ResolvedRepo: target.Repository, DefaultRef: target.Ref, TenantID: req.TenantID, Source: source}
	if req.ResolveCell != nil {
		return ResolveRegisteredTarget(ctx, out, req.ResolveCell)
	}
	return out, nil
}

func ResolveRegisteredTarget(ctx context.Context, target ResolvedTarget, resolve cellref.Resolver) (ResolvedTarget, error) {
	cell, err := cellref.ResolveTarget(ctx, resolve, target.CellName, target.CellRepo)
	if err != nil {
		return target, err
	}
	target.CellName, target.CellID, target.CellRepo = cell.Cell, cell.CellID, cell.CellRepo
	target.RepositorySource, target.ResolvedRepo = cell.CellRepo, cell.CellRepo
	return target, nil
}

// These functions normalize Git execution sources, which may use a separate
// local clone transport. Durable cell identity uses jobdb.NormalizeCellRepo.
func NormalizeRepositorySource(source string) (string, error) {
	return compiler.NormalizeGitRepositorySource(source)
}
func RepositorySourcesEqual(left, right string) (bool, error) {
	a, err := NormalizeRepositorySource(left)
	if err != nil {
		return false, err
	}
	b, err := NormalizeRepositorySource(right)
	return a == b, err
}
func cleanWorkingDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(dir)
}
