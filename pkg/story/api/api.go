package api

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrProjectNotFound = errors.New("storyapi: project not found")
	ErrCellNotFound    = errors.New("storyapi: cell not found")
)

type Project struct {
	ID            string  `json:"id"`
	GitRepoPath   string  `json:"git_repo_path"`
	GitRepoBranch *string `json:"git_repo_branch,omitempty"`
}

type ProjectService interface {
	GetProject(ctx context.Context, projectID string) (*Project, error)
}

type Cell struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	GitRepoName *string `json:"git_repo_name,omitempty"`
	GitBranch   *string `json:"git_branch,omitempty"`
}

type SearchFilter struct {
	ProjectIDs []string
	Names      []string
}

type CellIterator interface {
	Next(ctx context.Context) (*Cell, error)
	Close(ctx context.Context) error
}

type CellService interface {
	GetCell(ctx context.Context, cellID string) (*Cell, error)
	ListCells(ctx context.Context, filter SearchFilter) (CellIterator, error)
}

type MockProjectService struct {
	Projects       map[string]*Project
	GetProjectFunc func(ctx context.Context, projectID string) (*Project, error)
}

func NewMockProjectService(projects ...Project) *MockProjectService {
	byID := make(map[string]*Project, len(projects))
	for i := range projects {
		project := projects[i]
		byID[project.ID] = cloneProject(project)
	}
	return &MockProjectService{Projects: byID}
}

func (m *MockProjectService) GetProject(ctx context.Context, projectID string) (*Project, error) {
	if m != nil && m.GetProjectFunc != nil {
		return m.GetProjectFunc(ctx, projectID)
	}
	if m == nil || m.Projects == nil {
		return nil, ErrProjectNotFound
	}
	project, ok := m.Projects[projectID]
	if !ok || project == nil {
		return nil, ErrProjectNotFound
	}
	return cloneProject(*project), nil
}

type MockCellService struct {
	Cells         map[string]*Cell
	GetCellFunc   func(ctx context.Context, cellID string) (*Cell, error)
	ListCellsFunc func(ctx context.Context, filter SearchFilter) (CellIterator, error)
}

func NewMockCellService(cells ...Cell) *MockCellService {
	byID := make(map[string]*Cell, len(cells))
	for i := range cells {
		cell := cells[i]
		byID[cell.ID] = cloneCell(cell)
	}
	return &MockCellService{Cells: byID}
}

func (m *MockCellService) GetCell(ctx context.Context, cellID string) (*Cell, error) {
	if m != nil && m.GetCellFunc != nil {
		return m.GetCellFunc(ctx, cellID)
	}
	if m == nil || m.Cells == nil {
		return nil, ErrCellNotFound
	}
	cell, ok := m.Cells[cellID]
	if !ok || cell == nil {
		return nil, ErrCellNotFound
	}
	return cloneCell(*cell), nil
}

func (m *MockCellService) ListCells(ctx context.Context, filter SearchFilter) (CellIterator, error) {
	if m != nil && m.ListCellsFunc != nil {
		return m.ListCellsFunc(ctx, filter)
	}
	if m == nil {
		return &sliceCellIterator{}, nil
	}
	results := make([]*Cell, 0, len(m.Cells))
	for _, cell := range m.Cells {
		if cell == nil {
			continue
		}
		if len(filter.ProjectIDs) > 0 && !slices.Contains(filter.ProjectIDs, cell.ProjectID) {
			continue
		}
		if len(filter.Names) > 0 && !slices.Contains(filter.Names, cell.Name) {
			continue
		}
		results = append(results, cloneCell(*cell))
	}
	return &sliceCellIterator{cells: results}, nil
}

type sliceCellIterator struct {
	cells []*Cell
	index int
}

func (it *sliceCellIterator) Next(context.Context) (*Cell, error) {
	if it == nil || it.index >= len(it.cells) {
		return nil, ErrCellNotFound
	}
	cell := it.cells[it.index]
	it.index++
	if cell == nil {
		return nil, ErrCellNotFound
	}
	return cloneCell(*cell), nil
}

func (it *sliceCellIterator) Close(context.Context) error {
	return nil
}

func cloneProject(project Project) *Project {
	out := project
	if project.GitRepoBranch != nil {
		value := *project.GitRepoBranch
		out.GitRepoBranch = &value
	}
	return &out
}

func cloneCell(cell Cell) *Cell {
	out := cell
	if cell.GitRepoName != nil {
		value := *cell.GitRepoName
		out.GitRepoName = &value
	}
	if cell.GitBranch != nil {
		value := *cell.GitBranch
		out.GitBranch = &value
	}
	return &out
}
