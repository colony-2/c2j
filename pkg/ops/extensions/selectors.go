package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	gitpkg "github.com/colony-2/c2j/pkg/git"
	invschema "github.com/invopop/jsonschema"
	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
	yaml "gopkg.in/yaml.v3"
)

type ResolveOptions struct {
	BaseDir          string
	RepositorySource string
	RepositoryRef    string
}

type ResolvedSelectorPath struct {
	Selector         string
	ResolvedSelector string
	ResolvedCommit   string
	ProjectRoot      string
	Dir              string
}

type ResolvedOp struct {
	Selector         string
	ResolvedSelector string
	ResolvedCommit   string
	ProjectRoot      string
	OpDir            string
	SpecPath         string
	Spec             ExtensionOpSpec

	inputSchemaDoc  *invschema.Schema
	outputSchemaDoc *invschema.Schema
	compiledInput   *jsonschemav6.Schema
	compiledOutput  *jsonschemav6.Schema
	inputDefaults   *InputDefaults
}

func IsSelector(selector string) bool {
	return IsLocalSelector(selector) || isGitOpSelector(selector)
}

func IsLocalSelector(selector string) bool {
	selector = strings.TrimSpace(selector)
	return strings.HasPrefix(selector, "./") || strings.HasPrefix(selector, "../")
}

func Resolve(ctx context.Context, selector string, opts ResolveOptions) (*ResolvedOp, error) {
	resolvedPath, err := ResolvePath(ctx, selector, opts)
	if err != nil {
		return nil, err
	}
	return loadResolvedOpFromPath(resolvedPath)
}

func ResolvePath(ctx context.Context, selector string, opts ResolveOptions) (*ResolvedSelectorPath, error) {
	selector = strings.TrimSpace(selector)
	switch {
	case IsLocalSelector(selector):
		if strings.TrimSpace(opts.RepositorySource) != "" && strings.TrimSpace(opts.RepositoryRef) != "" {
			return resolveSameRepoSelectorPath(ctx, selector, opts)
		}
		return resolveLocalSelectorPath(selector, opts)
	case isGitOpSelector(selector):
		return resolveGitSelectorPath(ctx, selector)
	default:
		return nil, fmt.Errorf("unsupported extension selector %q", selector)
	}
}

func (r *ResolvedOp) SanitizeInvocationInputs(raw map[string]interface{}) (map[string]interface{}, *SandboxInput, error) {
	payload := map[string]interface{}{}
	for key, value := range raw {
		payload[key] = value
	}

	var sandbox *SandboxInput
	if rawSandbox, ok := payload["sandbox"]; ok {
		parsed, err := ParseSandboxInput(rawSandbox)
		if err != nil {
			return nil, nil, err
		}
		sandbox = parsed
		delete(payload, "sandbox")
	}
	return payload, sandbox, nil
}

func (r *ResolvedOp) ValidateInvocationInputs(raw map[string]interface{}) error {
	payload, _, err := r.SanitizeInvocationInputs(raw)
	if err != nil {
		return err
	}
	if r.compiledInput != nil {
		if err := r.compiledInput.Validate(payload); err != nil {
			return err
		}
		return nil
	}
	if r.inputSchemaDoc != nil {
		for _, req := range r.inputSchemaDoc.Required {
			if _, ok := payload[req]; !ok {
				return fmt.Errorf("missing required field: %s", req)
			}
		}
	}
	return nil
}

func (r *ResolvedOp) ApplyInvocationDefaults(raw map[string]interface{}) (bool, error) {
	if r == nil || r.inputDefaults == nil {
		return false, nil
	}
	payload, _, err := r.SanitizeInvocationInputs(raw)
	if err != nil {
		return false, err
	}
	changed, err := r.inputDefaults.Apply(payload)
	if err != nil {
		return false, err
	}
	for key, value := range payload {
		raw[key] = value
	}
	return changed, nil
}

func (r *ResolvedOp) WorkingDir() string {
	wd := strings.TrimSpace(r.Spec.WorkingDirectory)
	if wd == "" {
		return r.OpDir
	}
	if filepath.IsAbs(wd) {
		return wd
	}
	return filepath.Join(r.ProjectRoot, wd)
}

func (r *ResolvedOp) ZeroOutput() map[string]interface{} {
	return zeroObjectFromSchema(r.Spec.OutputSchema)
}

func resolveLocalSelectorPath(selector string, opts ResolveOptions) (*ResolvedSelectorPath, error) {
	baseDirs, err := resolveLocalBaseDirs(opts.BaseDir)
	if err != nil {
		return nil, err
	}
	var firstErr error
	for _, baseDir := range baseDirs {
		opDir := filepath.Clean(filepath.Join(baseDir, selector))
		rel, err := filepath.Rel(baseDir, opDir)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if firstErr == nil {
				firstErr = fmt.Errorf("local selector %q escapes base directory %q", selector, baseDir)
			}
			continue
		}
		if _, statErr := os.Stat(opDir); statErr == nil {
			return &ResolvedSelectorPath{
				Selector:    selector,
				ProjectRoot: baseDir,
				Dir:         opDir,
			}, nil
		} else if firstErr == nil {
			firstErr = statErr
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("failed to determine local selector base directory")
}

func resolveSameRepoSelectorPath(ctx context.Context, selector string, opts ResolveOptions) (*ResolvedSelectorPath, error) {
	repoURL, err := normalizeGitRepositorySource(opts.RepositorySource)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(opts.RepositoryRef)
	if ref == "" {
		return nil, fmt.Errorf("repository ref is required for same-repo selector %q", selector)
	}
	if strings.HasPrefix(selector, "../") {
		return nil, fmt.Errorf("same-repo selector %q must not escape the recipe source repo", selector)
	}
	opPath := strings.TrimPrefix(selector, "./")
	return resolveGitSelectorPath(ctx, fmt.Sprintf("git+%s//%s@%s", repoURL, path.Clean(opPath), ref))
}

func resolveLocalBaseDirs(baseDir string) ([]string, error) {
	candidates := []string{}
	if strings.TrimSpace(baseDir) != "" {
		candidates = append(candidates, baseDir)
	}
	if envRoot := strings.TrimSpace(os.Getenv("VIBETHIS_PROJECT_ROOT")); envRoot != "" {
		candidates = append(candidates, envRoot)
	}
	if found, err := findRepoRoot(""); err == nil && found != "" {
		candidates = append(candidates, found)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	seen := map[string]struct{}{}
	baseDirs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		baseDirs = append(baseDirs, abs)
	}
	if len(baseDirs) == 0 {
		return nil, fmt.Errorf("failed to determine local selector base directory")
	}
	return baseDirs, nil
}

func findRepoRoot(startDir string) (string, error) {
	if startDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = wd
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", nil
}

func resolveGitSelectorPath(ctx context.Context, selector string) (*ResolvedSelectorPath, error) {
	parsed, err := parseGitOpSelector(selector)
	if err != nil {
		return nil, err
	}

	cacheDir, commit, err := materializeGitSelector(ctx, parsed)
	if err != nil {
		return nil, err
	}
	return &ResolvedSelectorPath{
		Selector:         selector,
		ResolvedSelector: parsed.WithRef(commit),
		ResolvedCommit:   commit,
		ProjectRoot:      cacheDir,
		Dir:              filepath.Join(cacheDir, parsed.OpPath),
	}, nil
}

func materializeGitSelector(ctx context.Context, selector gitOpSelector) (string, string, error) {
	repoDir, err := os.MkdirTemp("", "extension-op-git-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp git checkout: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(repoDir) }

	repo := gitpkg.NewRepository(gitpkg.Config{})
	if err := repo.Clone(ctx, selector.RepositoryURL, repoDir, gitpkg.CloneOptions{}); err != nil {
		cleanup()
		return "", "", fmt.Errorf("clone git repo %q: %w", selector.RepositoryURL, err)
	}
	if err := repo.Checkout(ctx, repoDir, selector.Ref, gitpkg.CheckoutOptions{Detach: true}); err != nil {
		cleanup()
		return "", "", fmt.Errorf("checkout git ref %q: %w", selector.Ref, err)
	}
	commit, err := repo.GetCurrentCommit(ctx, repoDir)
	if err != nil {
		cleanup()
		return "", "", fmt.Errorf("resolve current git commit for %q: %w", selector.Raw, err)
	}

	cacheRoot, err := extensionCacheRoot()
	if err != nil {
		cleanup()
		return "", "", err
	}
	cacheDir := filepath.Join(cacheRoot, "git", selector.cacheKey(commit))
	if stat, err := os.Stat(cacheDir); err == nil && stat.IsDir() {
		cleanup()
		return cacheDir, commit, nil
	}
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o755); err != nil {
		cleanup()
		return "", "", fmt.Errorf("create extension cache parent: %w", err)
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		cleanup()
		return "", "", fmt.Errorf("clear extension cache dir: %w", err)
	}
	if err := os.Rename(repoDir, cacheDir); err != nil {
		if copyErr := copyDirTree(repoDir, cacheDir); copyErr != nil {
			cleanup()
			return "", "", fmt.Errorf("persist extension cache dir: %w", err)
		}
		cleanup()
	}
	return cacheDir, commit, nil
}

func extensionCacheRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for extension cache: %w", err)
	}
	root := filepath.Join(home, ".c2", "cache", "ops")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create extension cache root: %w", err)
	}
	return root, nil
}

func copyDirTree(src string, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDirTree(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src string, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func loadResolvedOpFromPath(path *ResolvedSelectorPath) (*ResolvedOp, error) {
	if path == nil {
		return nil, fmt.Errorf("resolved selector path is required")
	}
	return loadResolvedOp(path.Selector, path.ResolvedSelector, path.ResolvedCommit, path.ProjectRoot, path.Dir)
}

func loadResolvedOp(submittedSelector string, resolvedSelector string, resolvedCommit string, projectRoot string, opDir string) (*ResolvedOp, error) {
	specPath := filepath.Join(opDir, "op.yaml")
	if _, err := os.Stat(specPath); os.IsNotExist(err) {
		alt := filepath.Join(opDir, "op.yml")
		if _, err2 := os.Stat(alt); err2 == nil {
			specPath = alt
		} else {
			return nil, fmt.Errorf("extension selector %q missing op.yaml", submittedSelector)
		}
	}

	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", specPath, err)
	}

	var spec ExtensionOpSpec
	if err := yaml.Unmarshal(specBytes, &spec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", specPath, err)
	}

	resolved := &ResolvedOp{
		Selector:         submittedSelector,
		ResolvedSelector: resolvedSelector,
		ResolvedCommit:   resolvedCommit,
		ProjectRoot:      projectRoot,
		OpDir:            opDir,
		SpecPath:         specPath,
		Spec:             spec,
	}
	if strings.TrimSpace(resolved.Spec.Name) == "" {
		resolved.Spec.Name = filepath.Base(opDir)
	}
	if is := spec.InputSchema; is != nil {
		if doc, compiled, err := parseSchema(is); err == nil {
			resolved.inputSchemaDoc = doc
			resolved.compiledInput = compiled
			defaults, defaultsErr := BuildInputDefaults(is, compiled)
			if defaultsErr != nil {
				return nil, fmt.Errorf("extension selector %q input defaults are invalid: %w", submittedSelector, defaultsErr)
			}
			resolved.inputDefaults = defaults
		} else if schemaHasDefaultKeyword(is) {
			return nil, fmt.Errorf("extension selector %q input defaults require a valid schema: %w", submittedSelector, err)
		}
	}
	if oschema := spec.OutputSchema; oschema != nil {
		if doc, compiled, err := parseSchema(oschema); err == nil {
			resolved.outputSchemaDoc = doc
			resolved.compiledOutput = compiled
		}
	}
	return resolved, nil
}

func zeroObjectFromSchema(schema map[string]any) map[string]interface{} {
	if len(schema) == 0 {
		return map[string]interface{}{}
	}
	propsRaw, ok := schema["properties"].(map[string]any)
	if !ok || len(propsRaw) == 0 {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(propsRaw))
	for key, raw := range propsRaw {
		fieldSchema, _ := raw.(map[string]any)
		out[key] = zeroValueFromSchema(fieldSchema)
	}
	return out
}

func zeroValueFromSchema(schema map[string]any) interface{} {
	if len(schema) == 0 {
		return nil
	}
	switch schemaType := schema["type"].(type) {
	case string:
		switch schemaType {
		case "object":
			return zeroObjectFromSchema(schema)
		case "array":
			return []interface{}{}
		case "string":
			return ""
		case "boolean":
			return false
		case "integer", "number":
			return 0
		default:
			return nil
		}
	case []interface{}:
		for _, item := range schemaType {
			if s, ok := item.(string); ok && s != "null" {
				return zeroValueFromSchema(map[string]any{"type": s, "properties": schema["properties"]})
			}
		}
	}
	if propsRaw, ok := schema["properties"].(map[string]any); ok && len(propsRaw) > 0 {
		return zeroObjectFromSchema(schema)
	}
	return nil
}

type gitOpSelector struct {
	Raw           string
	RepositoryURL string
	OpPath        string
	Ref           string
}

func (s gitOpSelector) WithRef(ref string) string {
	return fmt.Sprintf("git+%s//%s@%s", s.RepositoryURL, s.OpPath, ref)
}

func (s gitOpSelector) cacheKey(commit string) string {
	sum := sha256.Sum256([]byte(s.RepositoryURL + "\n" + commit))
	return hex.EncodeToString(sum[:16])
}

func isGitOpSelector(selector string) bool {
	return strings.HasPrefix(strings.TrimSpace(selector), "git+")
}

func normalizeGitRepositorySource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("git repository source is required")
	}

	if isLikelyLocalPath(source) {
		absPath, err := filepath.Abs(source)
		if err != nil {
			return "", fmt.Errorf("resolve local repository path %q: %w", source, err)
		}
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String(), nil
	}
	if strings.HasPrefix(source, "git@") {
		return normalizeSCPGitRemote(source)
	}

	parsed, err := url.Parse(source)
	if err == nil && parsed.Scheme != "" {
		switch parsed.Scheme {
		case "file":
			absPath, absErr := filepath.Abs(parsed.Path)
			if absErr != nil {
				return "", fmt.Errorf("resolve file repository source %q: %w", source, absErr)
			}
			return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String(), nil
		case "http", "https", "ssh":
			return source, nil
		default:
			return "", fmt.Errorf("unsupported git repository scheme %q", parsed.Scheme)
		}
	}

	if looksLikeCanonicalRepositoryRef(source) {
		trimmed := strings.TrimSuffix(source, ".git")
		return "https://" + trimmed + ".git", nil
	}
	return "", fmt.Errorf("unsupported git repository source %q", source)
}

func isLikelyLocalPath(source string) bool {
	switch {
	case strings.HasPrefix(source, "/"):
		return true
	case source == "." || source == "..":
		return true
	case strings.HasPrefix(source, "./"), strings.HasPrefix(source, "../"):
		return true
	}
	if _, err := os.Stat(source); err == nil {
		return true
	}
	return false
}

func looksLikeCanonicalRepositoryRef(source string) bool {
	parts := strings.Split(strings.TrimSpace(source), "/")
	if len(parts) < 3 {
		return false
	}
	return strings.Contains(parts[0], ".")
}

func normalizeSCPGitRemote(source string) (string, error) {
	colonIdx := strings.Index(source, ":")
	if colonIdx <= 0 || colonIdx >= len(source)-1 {
		return "", fmt.Errorf("unsupported git repository source %q", source)
	}
	host := source[:colonIdx]
	repoPath := path.Clean(source[colonIdx+1:])
	if repoPath == "." || repoPath == ".." || strings.HasPrefix(repoPath, "../") {
		return "", fmt.Errorf("unsupported git repository source %q", source)
	}
	return "ssh://" + host + "/" + repoPath, nil
}

func parseGitOpSelector(selector string) (gitOpSelector, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return gitOpSelector{}, fmt.Errorf("git extension selector is required")
	}
	if !isGitOpSelector(selector) {
		return gitOpSelector{}, fmt.Errorf("git extension selector must start with git+")
	}

	atIdx := strings.LastIndex(selector, "@")
	if atIdx <= 0 || atIdx == len(selector)-1 {
		return gitOpSelector{}, fmt.Errorf("git extension selector %q must include a non-empty @<ref> suffix", selector)
	}
	beforeRef := selector[:atIdx]
	ref := strings.TrimSpace(selector[atIdx+1:])
	if ref == "" {
		return gitOpSelector{}, fmt.Errorf("git extension selector %q has an empty ref", selector)
	}

	delimIdx := strings.LastIndex(beforeRef, "//")
	if delimIdx <= 0 || delimIdx >= len(beforeRef)-2 {
		return gitOpSelector{}, fmt.Errorf("git extension selector %q must include //<repo-relative-path>", selector)
	}

	repositoryURL := beforeRef[:delimIdx]
	opPath := beforeRef[delimIdx+2:]
	if strings.TrimSpace(opPath) == "" {
		return gitOpSelector{}, fmt.Errorf("git extension selector %q has an empty op path", selector)
	}
	if strings.Contains(opPath, "\\") {
		return gitOpSelector{}, fmt.Errorf("git extension selector %q must use forward slashes in op paths", selector)
	}

	normalizedPath := path.Clean(opPath)
	if normalizedPath == "." || normalizedPath == ".." || strings.HasPrefix(normalizedPath, "../") || strings.HasPrefix(normalizedPath, "/") {
		return gitOpSelector{}, fmt.Errorf("git extension selector %q has an invalid op path %q", selector, opPath)
	}

	parsedURL, err := url.Parse(strings.TrimPrefix(repositoryURL, "git+"))
	if err != nil {
		return gitOpSelector{}, fmt.Errorf("parse git repository URL %q: %w", repositoryURL, err)
	}
	switch parsedURL.Scheme {
	case "file", "ssh", "http", "https":
	default:
		return gitOpSelector{}, fmt.Errorf("unsupported git extension selector scheme %q", parsedURL.Scheme)
	}

	return gitOpSelector{
		Raw:           selector,
		RepositoryURL: strings.TrimPrefix(repositoryURL, "git+"),
		OpPath:        normalizedPath,
		Ref:           ref,
	}, nil
}
