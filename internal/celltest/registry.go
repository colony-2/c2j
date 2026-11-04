package celltest

import (
	"context"
	"fmt"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Register explicitly provisions fixture identity before any job is submitted.
// Repeated setup may reuse the same mapping, but never repoints an existing cell.
func Register(ctx context.Context, rt jobdb.CellRuntime, tenant, name, repo string) (jobdb.Cell, error) {
	ctx = Context(ctx)
	repo = Repo(repo)
	page, err := rt.ListCells(ctx, jobdb.ListCellsRequest{TenantID: tenant, Name: name, State: "ALL"})
	if err != nil {
		return jobdb.Cell{}, err
	}
	if len(page.Cells) > 0 {
		cell := page.Cells[0]
		if cell.Repo != repo || cell.State != jobdb.CellActive {
			return jobdb.Cell{}, fmt.Errorf("fixture cell %q already registered with different repo or state", name)
		}
		return cell, nil
	}
	return rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: tenant, Name: name, Repo: repo})
}

// Create is the test assertion boundary for explicit fixture registration.
func Create(t interface {
	Helper()
	Fatalf(string, ...any)
}, target any, tenant, name, repo string) {
	t.Helper()
	var rt jobdb.CellRuntime
	switch value := target.(type) {
	case jobdb.CellRuntime:
		rt = value
	case interface{ CellRegistry() jobdb.CellRuntime }:
		rt = value.CellRegistry()
	}
	if rt == nil {
		t.Fatalf("fixture cell registration requires a registry, got %T", target)
		return
	}
	if _, err := Register(context.Background(), rt, tenant, name, repo); err != nil {
		t.Fatalf("register fixture cell %s/%s: %v", tenant, name, err)
	}
}
