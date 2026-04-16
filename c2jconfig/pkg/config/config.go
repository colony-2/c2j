package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

const (
	configDirName  = ".c2j"
	configFileName = "config.yaml"
)

var ErrConfigNotFound = errors.New("c2j config not found")

type SingleValueSource struct {
	Command string `yaml:"command"`
	Value   string `yaml:"value"`
	Parent  bool   `yaml:"parent"`
}

type MultiValueSource struct {
	Command string `yaml:"command"`
	Parent  bool   `yaml:"parent"`
}

type fileConfig struct {
	Parent        string            `yaml:"parent"`
	CellFilter    string            `yaml:"cell_filter"`
	Cells         MultiValueSource  `yaml:"cells"`
	RootCell      SingleValueSource `yaml:"root_cell"`
	CanonicalRepo SingleValueSource `yaml:"canonical_repo"`
	DefaultRef    SingleValueSource `yaml:"default_ref"`
}

type ProjectConfig struct {
	path    string
	rootDir string
	raw     fileConfig
}

func LoadProjectConfig(startDir string) (*ProjectConfig, error) {
	configPath, err := DiscoverConfigPath(startDir)
	if err != nil {
		return nil, err
	}
	return loadProjectConfig(configPath)
}

func DiscoverConfigPath(startDir string) (string, error) {
	startDir = strings.TrimSpace(startDir)
	if startDir == "" {
		return "", fmt.Errorf("start directory is required")
	}

	current, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve config search directory: %w", err)
	}

	for {
		candidate := filepath.Join(current, configDirName, configFileName)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", ErrConfigNotFound
		}
		current = parent
	}
}

func (c *ProjectConfig) Path() string {
	return c.path
}

func (c *ProjectConfig) RootDir() string {
	return c.rootDir
}

func (c *ProjectConfig) DependentCells(ctx context.Context) ([]string, error) {
	return c.resolveMultiValue(ctx, c.raw.Cells, c.parentDependentCells)
}

func (c *ProjectConfig) AllowedCells(ctx context.Context) ([]string, error) {
	cells, err := c.DependentCells(ctx)
	if err != nil {
		return nil, err
	}

	filter, err := c.cellFilter(ctx)
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return cells, nil
	}

	filtered := make([]string, 0, len(cells))
	for _, cell := range cells {
		if filter.MatchString(cell) {
			filtered = append(filtered, cell)
		}
	}
	return filtered, nil
}

func (c *ProjectConfig) RootCell(ctx context.Context) (string, error) {
	return c.resolveSingleValue(ctx, c.raw.RootCell, c.parentRootCell)
}

func (c *ProjectConfig) CanonicalRepo(ctx context.Context) (string, error) {
	return c.resolveSingleValue(ctx, c.raw.CanonicalRepo, c.parentCanonicalRepo)
}

func (c *ProjectConfig) DefaultRef(ctx context.Context) (string, error) {
	return c.resolveSingleValue(ctx, c.raw.DefaultRef, c.parentDefaultRef)
}

func (c *ProjectConfig) CanSubmitToCell(ctx context.Context, cell string) (bool, error) {
	cell = strings.TrimSpace(cell)
	if cell == "" {
		return false, nil
	}

	allowed, err := c.AllowedCells(ctx)
	if err != nil {
		return false, err
	}
	for _, allowedCell := range allowed {
		if allowedCell == cell {
			return true, nil
		}
	}
	return false, nil
}

func loadProjectConfig(configPath string) (*ProjectConfig, error) {
	rawBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}

	cfg := fileConfig{}
	if err := yaml.Unmarshal(rawBytes, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", configPath, err)
	}

	rootDir := filepath.Dir(filepath.Dir(configPath))
	return &ProjectConfig{
		path:    configPath,
		rootDir: rootDir,
		raw:     cfg,
	}, nil
}

func (c *ProjectConfig) resolveSingleValue(ctx context.Context, source SingleValueSource, parent func(context.Context) (string, error)) (string, error) {
	mode, err := c.singleMode(source)
	if err != nil {
		return "", err
	}

	switch mode {
	case "command":
		return c.runSingleCommand(ctx, source.Command)
	case "value":
		return strings.TrimSpace(source.Value), nil
	case "parent":
		return parent(ctx)
	default:
		return "", nil
	}
}

func (c *ProjectConfig) resolveMultiValue(ctx context.Context, source MultiValueSource, parent func(context.Context) ([]string, error)) ([]string, error) {
	mode, err := c.multiMode(source)
	if err != nil {
		return nil, err
	}

	switch mode {
	case "command":
		return c.runMultiCommand(ctx, source.Command)
	case "parent":
		return parent(ctx)
	default:
		return nil, nil
	}
}

func (c *ProjectConfig) singleMode(source SingleValueSource) (string, error) {
	setCount := 0
	if strings.TrimSpace(source.Command) != "" {
		setCount++
	}
	if strings.TrimSpace(source.Value) != "" {
		setCount++
	}
	if source.Parent {
		setCount++
	}
	if setCount > 1 {
		return "", fmt.Errorf("single-value config sources are mutually exclusive")
	}

	switch {
	case strings.TrimSpace(source.Command) != "":
		return "command", nil
	case strings.TrimSpace(source.Value) != "":
		return "value", nil
	case source.Parent:
		if strings.TrimSpace(c.raw.Parent) == "" {
			return "", fmt.Errorf("parent source requires a top-level parent")
		}
		return "parent", nil
	case strings.TrimSpace(c.raw.Parent) != "":
		return "parent", nil
	default:
		return "", nil
	}
}

func (c *ProjectConfig) multiMode(source MultiValueSource) (string, error) {
	setCount := 0
	if strings.TrimSpace(source.Command) != "" {
		setCount++
	}
	if source.Parent {
		setCount++
	}
	if setCount > 1 {
		return "", fmt.Errorf("multi-value config sources are mutually exclusive")
	}

	switch {
	case strings.TrimSpace(source.Command) != "":
		return "command", nil
	case source.Parent:
		if strings.TrimSpace(c.raw.Parent) == "" {
			return "", fmt.Errorf("parent source requires a top-level parent")
		}
		return "parent", nil
	case strings.TrimSpace(c.raw.Parent) != "":
		return "parent", nil
	default:
		return "", nil
	}
}

func (c *ProjectConfig) cellFilter(ctx context.Context) (*regexp.Regexp, error) {
	pattern := strings.TrimSpace(c.raw.CellFilter)
	if pattern == "" {
		parentPattern, err := c.parentCellFilter(ctx)
		if err != nil {
			return nil, err
		}
		pattern = strings.TrimSpace(parentPattern)
	}
	if pattern == "" {
		return nil, nil
	}

	filter, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile cell filter %q: %w", pattern, err)
	}
	return filter, nil
}

func (c *ProjectConfig) parentDependentCells(ctx context.Context) ([]string, error) {
	switch strings.TrimSpace(c.raw.Parent) {
	case "":
		return nil, nil
	case "go":
		modules, err := c.goListModules(ctx)
		if err != nil {
			return nil, err
		}

		self, err := c.parentCanonicalRepo(ctx)
		if err != nil {
			return nil, err
		}

		cells := make([]string, 0, len(modules))
		for _, module := range modules {
			if module.Main {
				continue
			}

			cell := normalizeModuleToCellRepo(module.Path)
			if cell == "" || cell == self {
				continue
			}
			cells = append(cells, cell)
		}
		return uniqueStrings(cells), nil
	default:
		return nil, fmt.Errorf("unsupported config parent %q", c.raw.Parent)
	}
}

func (c *ProjectConfig) parentRootCell(ctx context.Context) (string, error) {
	switch strings.TrimSpace(c.raw.Parent) {
	case "":
		return "", nil
	case "go":
		return c.parentCanonicalRepo(ctx)
	default:
		return "", fmt.Errorf("unsupported config parent %q", c.raw.Parent)
	}
}

func (c *ProjectConfig) parentCanonicalRepo(_ context.Context) (string, error) {
	switch strings.TrimSpace(c.raw.Parent) {
	case "":
		return "", nil
	case "go":
		goModPath := filepath.Join(c.rootDir, "go.mod")
		raw, err := os.ReadFile(goModPath)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", goModPath, err)
		}

		modulePath := strings.TrimSpace(modfile.ModulePath(raw))
		if modulePath == "" {
			return "", fmt.Errorf("go.mod in %s does not declare a module path", c.rootDir)
		}
		return normalizeModuleToCellRepo(modulePath), nil
	default:
		return "", fmt.Errorf("unsupported config parent %q", c.raw.Parent)
	}
}

func (c *ProjectConfig) parentDefaultRef(_ context.Context) (string, error) {
	switch strings.TrimSpace(c.raw.Parent) {
	case "":
		return "", nil
	case "go":
		return "main", nil
	default:
		return "", fmt.Errorf("unsupported config parent %q", c.raw.Parent)
	}
}

func (c *ProjectConfig) parentCellFilter(ctx context.Context) (string, error) {
	switch strings.TrimSpace(c.raw.Parent) {
	case "":
		return "", nil
	case "go":
		repo, err := c.parentCanonicalRepo(ctx)
		if err != nil {
			return "", err
		}
		return sameNamespacePattern(repo), nil
	default:
		return "", fmt.Errorf("unsupported config parent %q", c.raw.Parent)
	}
}

func (c *ProjectConfig) goListModules(ctx context.Context) ([]goModule, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-json", "all")
	cmd.Dir = c.rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run go list -m -json all in %s: %w (%s)", c.rootDir, err, strings.TrimSpace(string(out)))
	}

	decoder := json.NewDecoder(strings.NewReader(string(out)))
	modules := []goModule{}
	for {
		module := goModule{}
		if err := decoder.Decode(&module); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		modules = append(modules, module)
	}
	return modules, nil
}

func (c *ProjectConfig) runSingleCommand(ctx context.Context, command string) (string, error) {
	values, err := c.runMultiCommand(ctx, command)
	if err != nil {
		return "", err
	}
	switch len(values) {
	case 0:
		return "", nil
	case 1:
		return values[0], nil
	default:
		return "", fmt.Errorf("command %q returned multiple values for a single-value field", command)
	}
}

func (c *ProjectConfig) runMultiCommand(ctx context.Context, command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, nil
	}

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = c.rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run command %q in %s: %w (%s)", command, c.rootDir, err, strings.TrimSpace(string(out)))
	}
	return nonEmptyLines(string(out)), nil
}

func (c fileConfig) validate() error {
	switch strings.TrimSpace(c.Parent) {
	case "", "go":
	default:
		return fmt.Errorf("unsupported parent %q", c.Parent)
	}

	if err := validateSingleValueSource(c.RootCell); err != nil {
		return fmt.Errorf("root_cell: %w", err)
	}
	if err := validateSingleValueSource(c.CanonicalRepo); err != nil {
		return fmt.Errorf("canonical_repo: %w", err)
	}
	if err := validateSingleValueSource(c.DefaultRef); err != nil {
		return fmt.Errorf("default_ref: %w", err)
	}
	if err := validateMultiValueSource(c.Cells); err != nil {
		return fmt.Errorf("cells: %w", err)
	}
	if pattern := strings.TrimSpace(c.CellFilter); pattern != "" {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("cell_filter: %w", err)
		}
	}
	return nil
}

func validateSingleValueSource(source SingleValueSource) error {
	setCount := 0
	if strings.TrimSpace(source.Command) != "" {
		setCount++
	}
	if strings.TrimSpace(source.Value) != "" {
		setCount++
	}
	if source.Parent {
		setCount++
	}
	if setCount > 1 {
		return fmt.Errorf("command, value, and parent are mutually exclusive")
	}
	return nil
}

func validateMultiValueSource(source MultiValueSource) error {
	setCount := 0
	if strings.TrimSpace(source.Command) != "" {
		setCount++
	}
	if source.Parent {
		setCount++
	}
	if setCount > 1 {
		return fmt.Errorf("command and parent are mutually exclusive")
	}
	return nil
}

func nonEmptyLines(raw string) []string {
	lines := strings.Split(raw, "\n")
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		values = append(values, line)
	}
	return uniqueStrings(values)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func normalizeModuleToCellRepo(modulePath string) string {
	modulePath = strings.TrimSpace(modulePath)
	if modulePath == "" {
		return ""
	}

	parts := strings.Split(modulePath, "/")
	last := parts[len(parts)-1]
	if isGoMajorVersionSegment(last) && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, "/")
}

func sameNamespacePattern(repo string) string {
	repo = normalizeModuleToCellRepo(repo)
	idx := strings.LastIndex(repo, "/")
	if idx <= 0 {
		return ""
	}
	namespace := repo[:idx]
	return "^" + regexp.QuoteMeta(namespace) + "/[^/]+$"
}

func isGoMajorVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	if segment == "v0" || segment == "v1" {
		return false
	}
	for _, ch := range segment[1:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

type goModule struct {
	Path string `json:"Path"`
	Main bool   `json:"Main"`
}
