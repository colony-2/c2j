package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"gopkg.in/yaml.v3"
)

const (
	configDirName       = ".c2j"
	configFileName      = "config.yaml"
	cellPlaceholder     = "${{ cell }}"
	shortNamePatternRaw = `^[A-Za-z0-9._-]+$`
	shortNameGroupRaw   = `([A-Za-z0-9._-]+)`
)

var (
	ErrConfigNotFound = errors.New("c2j config not found")
	shortNamePattern  = regexp.MustCompile(shortNamePatternRaw)
)

type StringOrCommand struct {
	Value   string
	Command string
}

type DependentsConfig struct {
	mode    string
	Repos   []string
	Command string `yaml:"command"`
	Filter  string `yaml:"filter"`
}

type CellRefConfig struct {
	Repo StringOrCommand `yaml:"repo"`
	Ref  StringOrCommand `yaml:"ref"`
}

type fileConfig struct {
	Base       string           `yaml:"base"`
	Dependents DependentsConfig `yaml:"dependents"`
	Self       CellRefConfig    `yaml:"self"`
	Root       CellRefConfig    `yaml:"root"`
	Pattern    string           `yaml:"pattern"`
}

type ProjectConfig struct {
	path    string
	rootDir string
	raw     fileConfig
	source  string
}

type autoDetectedProjectConfig struct {
	rootDir string
	raw     fileConfig
	source  string
}

type projectConfigDetector struct {
	name   string
	detect func(startDir string) (*autoDetectedProjectConfig, error)
}

var projectConfigDetectors = []projectConfigDetector{
	{name: "go", detect: detectGoProjectConfig},
}

func (s *StringOrCommand) UnmarshalYAML(node *yaml.Node) error {
	*s = StringOrCommand{}

	if node == nil || node.Kind == 0 {
		return nil
	}

	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return nil
		}
		s.Value = strings.TrimSpace(node.Value)
		return nil
	case yaml.MappingNode:
		if len(node.Content) == 0 {
			return nil
		}
		if len(node.Content) != 2 {
			return fmt.Errorf("expected a scalar string or mapping with command")
		}
		key := strings.TrimSpace(node.Content[0].Value)
		if key != "command" {
			return fmt.Errorf("expected a scalar string or mapping with command")
		}
		s.Command = strings.TrimSpace(node.Content[1].Value)
		return nil
	default:
		return fmt.Errorf("expected a scalar string or mapping with command")
	}
}

func (d *DependentsConfig) UnmarshalYAML(node *yaml.Node) error {
	*d = DependentsConfig{}

	if node == nil || node.Kind == 0 {
		return nil
	}

	switch node.Kind {
	case yaml.SequenceNode:
		d.mode = "list"
		d.Repos = make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return fmt.Errorf("expected dependents list entries to be scalar repo values")
			}
			d.Repos = append(d.Repos, strings.TrimSpace(item.Value))
		}
		return nil
	case yaml.MappingNode:
		d.mode = "object"
		type rawDependentsConfig struct {
			Command string `yaml:"command"`
			Filter  string `yaml:"filter"`
		}
		raw := rawDependentsConfig{}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		d.Command = strings.TrimSpace(raw.Command)
		d.Filter = strings.TrimSpace(raw.Filter)
		return nil
	default:
		return fmt.Errorf("expected dependents to be either a list of repos or an object")
	}
}

func LoadProjectConfig(startDir string) (*ProjectConfig, error) {
	configPath, err := DiscoverConfigPath(startDir)
	if err == nil {
		return loadProjectConfig(configPath)
	}
	if !errors.Is(err, ErrConfigNotFound) {
		return nil, err
	}

	detected, err := autoDetectProjectConfig(startDir)
	if err != nil {
		return nil, err
	}
	return &ProjectConfig{
		rootDir: detected.rootDir,
		raw:     detected.raw,
		source:  detected.source,
	}, nil
}

func DiscoverConfigPath(startDir string) (string, error) {
	return discoverAncestorFile(startDir, filepath.Join(configDirName, configFileName))
}

func (c *ProjectConfig) Path() string {
	return c.path
}

func (c *ProjectConfig) RootDir() string {
	return c.rootDir
}

func (c *ProjectConfig) SelfRepo(ctx context.Context) (string, error) {
	return c.resolveRepoValue(ctx, c.raw.Self.Repo, c.baseSelfRepo)
}

func (c *ProjectConfig) SelfRef(ctx context.Context) (string, error) {
	ref, err := c.resolveScalar(ctx, c.raw.Self.Ref)
	if err != nil {
		return "", fmt.Errorf("self.ref: %w", err)
	}
	if strings.TrimSpace(ref) == "" {
		return compiler.DefaultRecipeRef, nil
	}
	return ref, nil
}

func (c *ProjectConfig) RootRepo(ctx context.Context) (string, error) {
	repo, err := c.resolveRepoValue(ctx, c.raw.Root.Repo, nil)
	if err != nil {
		return "", fmt.Errorf("root.repo: %w", err)
	}
	if strings.TrimSpace(repo) != "" {
		return repo, nil
	}

	pattern, err := c.pattern(ctx)
	if err != nil {
		return "", err
	}
	if pattern == nil {
		return "", nil
	}
	return pattern.Expand("root")
}

func (c *ProjectConfig) RootRef(ctx context.Context) (string, error) {
	ref, err := c.resolveScalar(ctx, c.raw.Root.Ref)
	if err != nil {
		return "", fmt.Errorf("root.ref: %w", err)
	}
	if strings.TrimSpace(ref) != "" {
		return ref, nil
	}
	return c.SelfRef(ctx)
}

func (c *ProjectConfig) DependentRepos(ctx context.Context) ([]string, error) {
	switch {
	case c.raw.Dependents.mode == "list":
		values := uniqueStrings(c.raw.Dependents.Repos)
		for _, value := range values {
			if !isRepoValue(value) {
				return nil, fmt.Errorf("dependents list entry %q is not a repo", value)
			}
		}
		return values, nil
	case strings.TrimSpace(c.raw.Dependents.Command) != "":
		values, err := c.runMultiCommand(ctx, c.raw.Dependents.Command)
		if err != nil {
			return nil, fmt.Errorf("dependents.command: %w", err)
		}
		for _, value := range values {
			if !isRepoValue(value) {
				return nil, fmt.Errorf("dependents.command emitted %q, expected repos one per line", value)
			}
		}
		return values, nil
	case strings.TrimSpace(c.raw.Base) == "go":
		return c.baseDependentRepos(ctx)
	default:
		return nil, nil
	}
}

func (c *ProjectConfig) AllowedDependentRepos(ctx context.Context) ([]string, error) {
	repos, err := c.DependentRepos(ctx)
	if err != nil {
		return nil, err
	}

	filter, err := c.dependentFilter(ctx)
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return repos, nil
	}

	filtered := make([]string, 0, len(repos))
	for _, repo := range repos {
		if filter.MatchString(repo) {
			filtered = append(filtered, repo)
		}
	}
	return filtered, nil
}

func (c *ProjectConfig) Pattern(ctx context.Context) (string, error) {
	pattern, err := c.pattern(ctx)
	if err != nil {
		return "", err
	}
	if pattern == nil {
		return "", nil
	}
	return pattern.raw, nil
}

func (c *ProjectConfig) ExpandCellName(ctx context.Context, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("cell value is required")
	}

	if value == "root" {
		rootRepo, err := c.RootRepo(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(rootRepo) != "" {
			return rootRepo, nil
		}
		return "", fmt.Errorf("root.repo is required when pattern is not available")
	}

	if isRepoValue(value) {
		return value, nil
	}
	if !isShortName(value) {
		return "", fmt.Errorf("cell %q is neither a repo nor a valid short name", value)
	}

	pattern, err := c.pattern(ctx)
	if err != nil {
		return "", err
	}
	if pattern == nil {
		return "", fmt.Errorf("short name %q requires pattern", value)
	}
	return pattern.Expand(value)
}

func (c *ProjectConfig) CellNameFromRepo(ctx context.Context, repo string) (string, bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", false
	}

	rootRepo, err := c.RootRepo(ctx)
	if err == nil && rootRepo != "" && repoValuesEqual(rootRepo, repo) {
		return "root", true
	}

	pattern, err := c.pattern(ctx)
	if err != nil || pattern == nil {
		return "", false
	}
	return pattern.Match(repo)
}

func (c *ProjectConfig) CanSubmitToCell(ctx context.Context, value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, nil
	}

	repo, err := c.ExpandCellName(ctx, value)
	if err != nil {
		return false, err
	}

	allowed, err := c.AllowedDependentRepos(ctx)
	if err != nil {
		return false, err
	}
	for _, allowedRepo := range allowed {
		if repoValuesEqual(allowedRepo, repo) {
			return true, nil
		}
	}
	return false, nil
}

func (c *ProjectConfig) CanonicalRepo(ctx context.Context) (string, error) {
	return c.SelfRepo(ctx)
}

func (c *ProjectConfig) DefaultRef(ctx context.Context) (string, error) {
	return c.SelfRef(ctx)
}

func (c *ProjectConfig) RootCell(ctx context.Context) (string, error) {
	return c.RootRepo(ctx)
}

func (c *ProjectConfig) DependentCells(ctx context.Context) ([]string, error) {
	return c.DependentRepos(ctx)
}

func (c *ProjectConfig) AllowedCells(ctx context.Context) ([]string, error) {
	return c.AllowedDependentRepos(ctx)
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
		source:  "file",
	}, nil
}

func (c *ProjectConfig) resolveRepoValue(ctx context.Context, source StringOrCommand, fallback func(context.Context) (string, error)) (string, error) {
	value, err := c.resolveScalar(ctx, source)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" && fallback != nil {
		value, err = fallback(ctx)
		if err != nil {
			return "", err
		}
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if isRepoValue(value) {
		return value, nil
	}
	if !isShortName(value) {
		return "", fmt.Errorf("repo value %q is neither a repo nor a valid short name", value)
	}

	pattern, err := c.pattern(ctx)
	if err != nil {
		return "", err
	}
	if pattern == nil {
		return "", fmt.Errorf("short name %q requires pattern", value)
	}
	return pattern.Expand(value)
}

func (c *ProjectConfig) resolveScalar(ctx context.Context, source StringOrCommand) (string, error) {
	switch {
	case strings.TrimSpace(source.Command) != "":
		return c.runSingleCommand(ctx, source.Command)
	default:
		return strings.TrimSpace(source.Value), nil
	}
}

func (c *ProjectConfig) dependentFilter(ctx context.Context) (*regexp.Regexp, error) {
	if c.raw.Dependents.mode == "list" {
		return nil, nil
	}

	pattern := strings.TrimSpace(c.raw.Dependents.Filter)
	if pattern == "" {
		cellPattern, err := c.pattern(ctx)
		if err != nil {
			return nil, err
		}
		if cellPattern != nil {
			pattern = cellPattern.Regex()
		}
	}
	if pattern == "" {
		return nil, nil
	}

	filter, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile dependents.filter %q: %w", pattern, err)
	}
	return filter, nil
}

func (c *ProjectConfig) pattern(ctx context.Context) (*cellPattern, error) {
	raw := strings.TrimSpace(c.raw.Pattern)
	if raw == "" && strings.TrimSpace(c.raw.Base) == "go" {
		repo, err := c.baseSelfRepo(ctx)
		if err != nil {
			return nil, err
		}
		var ok bool
		raw, ok = derivePatternFromRepo(repo)
		if !ok {
			return nil, nil
		}
	}
	if raw == "" {
		return nil, nil
	}
	return parseCellPattern(raw)
}

func (c *ProjectConfig) baseSelfRepo(ctx context.Context) (string, error) {
	switch strings.TrimSpace(c.raw.Base) {
	case "":
		return "", nil
	case "go":
		return goCurrentRepoName(ctx, c.rootDir)
	default:
		return "", fmt.Errorf("unsupported config base %q", c.raw.Base)
	}
}

func (c *ProjectConfig) baseDependentRepos(ctx context.Context) ([]string, error) {
	switch strings.TrimSpace(c.raw.Base) {
	case "":
		return nil, nil
	case "go":
		return goDependentRepoNames(ctx, c.rootDir)
	default:
		return nil, fmt.Errorf("unsupported config base %q", c.raw.Base)
	}
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
	switch strings.TrimSpace(c.Base) {
	case "", "go":
	default:
		return fmt.Errorf("unsupported base %q", c.Base)
	}

	if err := c.Self.Repo.validate("self.repo"); err != nil {
		return err
	}
	if err := c.Self.Ref.validate("self.ref"); err != nil {
		return err
	}
	if err := c.Root.Repo.validate("root.repo"); err != nil {
		return err
	}
	if err := c.Root.Ref.validate("root.ref"); err != nil {
		return err
	}
	for _, repo := range c.Dependents.Repos {
		if strings.TrimSpace(repo) == "" {
			return fmt.Errorf("dependents: list entries must not be empty")
		}
		if !isRepoValue(repo) {
			return fmt.Errorf("dependents: list entry %q is not a repo", repo)
		}
	}
	if pattern := strings.TrimSpace(c.Dependents.Filter); pattern != "" {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("dependents.filter: %w", err)
		}
	}
	if c.Dependents.mode == "list" && strings.TrimSpace(c.Dependents.Filter) != "" {
		return fmt.Errorf("dependents.filter is not supported when dependents is a list")
	}
	if c.Dependents.mode == "list" && strings.TrimSpace(c.Dependents.Command) != "" {
		return fmt.Errorf("dependents.command is not supported when dependents is a list")
	}
	if pattern := strings.TrimSpace(c.Pattern); pattern != "" {
		if _, err := parseCellPattern(pattern); err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
	}
	return nil
}

func (s StringOrCommand) validate(field string) error {
	switch {
	case strings.TrimSpace(s.Value) != "" && strings.TrimSpace(s.Command) != "":
		return fmt.Errorf("%s: scalar values and command sources are mutually exclusive", field)
	default:
		return nil
	}
}

type cellPattern struct {
	raw             string
	prefix          string
	suffix          string
	rawRegex        *regexp.Regexp
	normalizedRegex *regexp.Regexp
}

func parseCellPattern(raw string) (*cellPattern, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.Count(raw, cellPlaceholder) != 1 {
		return nil, fmt.Errorf("pattern must contain exactly one %q placeholder", cellPlaceholder)
	}

	parts := strings.Split(raw, cellPlaceholder)
	pattern := &cellPattern{
		raw:    raw,
		prefix: parts[0],
		suffix: parts[1],
		rawRegex: regexp.MustCompile(
			"^" + regexp.QuoteMeta(parts[0]) + shortNameGroupRaw + regexp.QuoteMeta(parts[1]) + "$",
		),
	}
	pattern.normalizedRegex = buildNormalizedPatternRegex(raw)
	return pattern, nil
}

func (p *cellPattern) Expand(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !isShortName(name) {
		return "", fmt.Errorf("short name %q is invalid", name)
	}
	return p.prefix + name + p.suffix, nil
}

func (p *cellPattern) Match(repo string) (string, bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", false
	}

	if matches := p.rawRegex.FindStringSubmatch(repo); len(matches) == 2 {
		return matches[1], true
	}

	if p.normalizedRegex != nil {
		normalizedRepo, err := compiler.NormalizeGitRepositorySource(repo)
		if err == nil {
			if matches := p.normalizedRegex.FindStringSubmatch(normalizedRepo); len(matches) == 2 {
				return matches[1], true
			}
		}
	}

	return "", false
}

func (p *cellPattern) Regex() string {
	return "^" + regexp.QuoteMeta(p.prefix) + shortNameGroupRaw + regexp.QuoteMeta(p.suffix) + "$"
}

func buildNormalizedPatternRegex(raw string) *regexp.Regexp {
	const token = "c2jcelltoken"

	withToken := strings.Replace(raw, cellPlaceholder, token, 1)
	normalized, err := compiler.NormalizeGitRepositorySource(withToken)
	if err != nil {
		return nil
	}

	parts := strings.Split(normalized, token)
	if len(parts) != 2 {
		return nil
	}
	return regexp.MustCompile("^" + regexp.QuoteMeta(parts[0]) + shortNameGroupRaw + regexp.QuoteMeta(parts[1]) + "$")
}

func derivePatternFromRepo(repo string) (string, bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", false
	}

	name := compiler.RepositoryNameFromSource(repo)
	if strings.TrimSpace(name) == "" {
		return "", false
	}

	replacement := cellPlaceholder
	if dash := strings.Index(name, "-"); dash >= 0 {
		replacement = name[:dash+1] + cellPlaceholder
	}

	idx := strings.LastIndex(repo, name)
	if idx < 0 {
		return "", false
	}
	return repo[:idx] + replacement + repo[idx+len(name):], true
}

func goCurrentRepoName(ctx context.Context, rootDir string) (string, error) {
	values, err := runGoList(ctx, rootDir, "{{.Path}}")
	if err != nil {
		return "", err
	}
	switch len(values) {
	case 0:
		return "", fmt.Errorf("go list did not return the main module path")
	case 1:
		return normalizeModuleRepoName(values[0]), nil
	default:
		return "", fmt.Errorf("go list returned multiple module paths for the current repo")
	}
}

func goDependentRepoNames(ctx context.Context, rootDir string) ([]string, error) {
	values, err := runGoList(ctx, rootDir, "{{if not .Main}}{{.Path}}{{end}}", "all")
	if err != nil {
		return nil, err
	}

	repos := make([]string, 0, len(values))
	for _, value := range values {
		repo := normalizeModuleRepoName(value)
		if strings.TrimSpace(repo) == "" {
			continue
		}
		repos = append(repos, repo)
	}
	return uniqueStrings(repos), nil
}

func autoDetectProjectConfig(startDir string) (*autoDetectedProjectConfig, error) {
	for _, detector := range projectConfigDetectors {
		detected, err := detector.detect(startDir)
		if err != nil {
			return nil, err
		}
		if detected != nil {
			if strings.TrimSpace(detected.source) == "" {
				detected.source = detector.name
			}
			return detected, nil
		}
	}
	return nil, ErrConfigNotFound
}

func detectGoProjectConfig(startDir string) (*autoDetectedProjectConfig, error) {
	goModPath, err := discoverAncestorFile(startDir, "go.mod")
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			return nil, nil
		}
		return nil, err
	}

	return &autoDetectedProjectConfig{
		rootDir: filepath.Dir(goModPath),
		raw: fileConfig{
			Base: "go",
		},
		source: "auto:go",
	}, nil
}

func discoverAncestorFile(startDir string, relativePath string) (string, error) {
	startDir = strings.TrimSpace(startDir)
	if startDir == "" {
		return "", fmt.Errorf("start directory is required")
	}

	current, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve config search directory: %w", err)
	}

	for {
		candidate := filepath.Join(current, relativePath)
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

func runGoList(ctx context.Context, rootDir string, format string, extraArgs ...string) ([]string, error) {
	args := []string{"list", "-m", "-f", format}
	args = append(args, extraArgs...)

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run %q in %s: %w (%s)", strings.Join(append([]string{"go"}, args...), " "), rootDir, err, strings.TrimSpace(string(out)))
	}
	return nonEmptyLines(string(out)), nil
}

func repoValuesEqual(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == right {
		return true
	}

	leftNormalized, leftErr := compiler.NormalizeGitRepositorySource(left)
	rightNormalized, rightErr := compiler.NormalizeGitRepositorySource(right)
	return leftErr == nil && rightErr == nil && leftNormalized == rightNormalized
}

func isRepoValue(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, ":") || strings.Contains(value, "/")
}

func isShortName(value string) bool {
	return shortNamePattern.MatchString(strings.TrimSpace(value))
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

func normalizeModuleRepoName(modulePath string) string {
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
