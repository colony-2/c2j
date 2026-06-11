package testjob

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	configpkg "github.com/colony-2/c2j/pkg/config"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/recipetest"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"gopkg.in/yaml.v3"
)

var embedEnvMu sync.Mutex

type suiteEnvelope struct {
	Cases []map[string]interface{} `json:"cases" yaml:"cases"`
}

type CompiledIR struct {
	TargetRecipe recipetest.TargetRecipe `json:"target_recipe" yaml:"target_recipe"`
	Cases        []recipetest.Case       `json:"cases" yaml:"cases"`
}

type CaseResult struct {
	CaseID     string                       `json:"case_id"`
	Status     string                       `json:"status"`
	DurationMs int64                        `json:"duration_ms"`
	Validation *recipetest.ValidationResult `json:"validation,omitempty"`
	Run        *recipetest.CaseRunResult    `json:"run,omitempty"`
	Error      string                       `json:"error,omitempty"`
}

type validationSummary struct {
	Cases          int          `json:"cases"`
	InvalidOrError int          `json:"invalid_or_error"`
	Results        []CaseResult `json:"results,omitempty"`
}

func Compile(ctx context.Context, opts Options) (CompiledIR, error) {
	var err error
	opts, err = completeOptions(ctx, opts)
	if err != nil {
		return CompiledIR{}, err
	}
	return compileCompleted(ctx, opts)
}

func completeOptions(ctx context.Context, opts Options) (Options, error) {
	if err := opts.Complete(ctx); err != nil {
		return opts, exitError{code: exitCodeFailure, err: err}
	}
	if err := opts.ValidateSuiteInput(); err != nil {
		return opts, exitError{code: exitCodeUsage, err: err}
	}
	return opts, nil
}

func compileCompleted(ctx context.Context, opts Options) (CompiledIR, error) {
	c2jops.Register()
	target, err := buildTargetRecipe(ctx, opts)
	if err != nil {
		return CompiledIR{}, exitError{code: exitCodeCompile, err: err}
	}
	raw, format, err := loadSuiteBytes(opts)
	if err != nil {
		return CompiledIR{}, exitError{code: exitCodeCompile, err: err}
	}
	cases, err := parseSuiteCases(raw, format)
	if err != nil {
		return CompiledIR{}, exitError{code: exitCodeCompile, err: err}
	}
	cases = filterCasesByIDs(cases, opts.CaseIDs)
	if len(cases) == 0 {
		return CompiledIR{}, exitError{code: exitCodeCompile, err: fmt.Errorf("no cases selected")}
	}
	return CompiledIR{TargetRecipe: target, Cases: cases}, nil
}

func CompileAndWrite(ctx context.Context, opts Options) error {
	var err error
	opts, err = completeOptions(ctx, opts)
	if err != nil {
		return err
	}
	ir, err := compileCompleted(ctx, opts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.OutPath) == "" {
		return exitError{code: exitCodeUsage, err: fmt.Errorf("--out is required")}
	}
	outPath, err := absPathFromWorkingDir(opts.WorkingDir, opts.OutPath)
	if err != nil {
		return exitError{code: exitCodeUsage, err: err}
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	b, err := json.MarshalIndent(ir, "", "  ")
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if opts.JSONOutput {
		return json.NewEncoder(opts.Stdout).Encode(ir)
	}
	_, err = fmt.Fprintf(opts.Stdout, "compiled %d case(s) to %s\n", len(ir.Cases), outPath)
	return err
}

func Validate(ctx context.Context, opts Options) error {
	var err error
	opts, err = completeOptions(ctx, opts)
	if err != nil {
		return err
	}
	ir, err := compileCompleted(ctx, opts)
	if err != nil {
		return err
	}
	results := runCases(ctx, opts, ir, false)
	invalid := countByStatus(results, "invalid") + countByStatus(results, "error")
	summary := validationSummary{Cases: len(results), InvalidOrError: invalid, Results: results}
	if opts.JSONOutput {
		if err := json.NewEncoder(opts.Stdout).Encode(summary); err != nil {
			return exitError{code: exitCodeFailure, err: err}
		}
	}
	if invalid > 0 {
		return exitError{code: exitCodeCases, err: fmt.Errorf("%d case(s) invalid or errored", invalid)}
	}
	return nil
}

func Run(ctx context.Context, opts Options) error {
	var err error
	opts, err = completeOptions(ctx, opts)
	if err != nil {
		return err
	}
	ir, err := compileCompleted(ctx, opts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.OutDir) == "" {
		opts.OutDir = defaultOutDir()
	}
	outDir, err := absPathFromWorkingDir(opts.WorkingDir, opts.OutDir)
	if err != nil {
		return exitError{code: exitCodeUsage, err: err}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}

	results := runCases(ctx, opts, ir, true)
	if err := writeRunArtifacts(outDir, results); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if strings.TrimSpace(opts.JSONLEvents) != "" {
		jsonlPath, err := absPathFromWorkingDir(opts.WorkingDir, opts.JSONLEvents)
		if err != nil {
			return exitError{code: exitCodeUsage, err: err}
		}
		if err := writeJSONLEvents(jsonlPath, results); err != nil {
			return exitError{code: exitCodeFailure, err: err}
		}
	}
	failed := countByStatus(results, "failed") + countByStatus(results, "error") + countByStatus(results, "invalid") + countByStatus(results, "timed_out")
	if failed > 0 {
		return exitError{code: exitCodeCases, err: fmt.Errorf("%d case(s) failed", failed)}
	}
	return nil
}

func runCases(ctx context.Context, opts Options, ir CompiledIR, execute bool) []CaseResult {
	c2jops.Register()
	harnessOpts, cleanup, err := buildHarnessOptions(ctx, opts, ir)
	if err != nil {
		return []CaseResult{{CaseID: "setup", Status: "error", Error: err.Error()}}
	}
	defer cleanup()

	parallelism := opts.Parallelism
	if parallelism <= 0 {
		parallelism = 1
	}
	stopOnFailure := opts.FailFast
	if execute {
		stopOnFailure = opts.StopOnFailure
	}

	type workItem struct {
		caseObj recipetest.Case
	}
	jobs := make(chan workItem)
	results := make(chan CaseResult, len(ir.Cases))
	var stop int32
	var wg sync.WaitGroup
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				if atomic.LoadInt32(&stop) == 1 {
					continue
				}
				result := runOneCase(ctx, opts, harnessOpts, ir.TargetRecipe, item.caseObj, execute)
				if stopOnFailure && isFailureStatus(result.Status) {
					atomic.StoreInt32(&stop, 1)
				}
				results <- result
			}
		}()
	}

	go func() {
		for _, c := range ir.Cases {
			if atomic.LoadInt32(&stop) == 1 {
				break
			}
			jobs <- workItem{caseObj: c}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	out := make([]CaseResult, 0, len(ir.Cases))
	for r := range results {
		fmt.Fprintf(opts.Stdout, "%s %s (%dms)\n", r.CaseID, r.Status, r.DurationMs)
		if !opts.JSONOutput {
			writeCaseResultDiagnostics(opts.Stdout, r)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CaseID < out[j].CaseID })
	return out
}

func writeCaseResultDiagnostics(w io.Writer, r CaseResult) {
	if strings.TrimSpace(r.Error) != "" {
		fmt.Fprintf(w, "  - error: %s\n", r.Error)
	}
	if r.Validation == nil {
		return
	}
	for _, issue := range r.Validation.Errors {
		fmt.Fprintf(w, "  - %s\n", formatValidationIssue(issue))
	}
}

func formatValidationIssue(issue recipetest.Issue) string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(issue.Code) != "" {
		parts = append(parts, issue.Code)
	}
	if strings.TrimSpace(issue.Field) != "" {
		parts = append(parts, issue.Field+":")
	}
	if strings.TrimSpace(issue.Message) != "" {
		parts = append(parts, issue.Message)
	}
	return strings.Join(parts, " ")
}

func runOneCase(ctx context.Context, opts Options, harnessOpts recipetest.HarnessOptions, target recipetest.TargetRecipe, c recipetest.Case, execute bool) CaseResult {
	start := time.Now()
	caseID := c.ID
	if strings.TrimSpace(harnessOpts.WorkRoot) == "" {
		harnessOpts.WorkRoot = filepath.Join(os.TempDir(), "c2j-recipe-tests")
	}
	harnessOpts.WorkRoot = filepath.Join(harnessOpts.WorkRoot, sanitizeCaseID(caseID))
	result := CaseResult{CaseID: caseID}
	if execute {
		exec := recipetest.ExecutionOptions{
			Mode:             "isolated",
			Timeout:          opts.Execution.Timeout,
			ArtifactMode:     opts.Execution.ArtifactMode,
			ArtifactMaxBytes: opts.Execution.ArtifactMaxBytes,
			EvaluationMode:   opts.Execution.EvaluationMode,
		}
		run := recipetest.RunCase(ctx, harnessOpts, opts.TenantID, target, c, exec)
		result.Status = run.Status
		result.Run = &run
	} else {
		validation := recipetest.ValidateCase(ctx, harnessOpts, opts.TenantID, target, c)
		if validation.Valid {
			result.Status = "valid"
		} else {
			result.Status = "invalid"
		}
		result.Validation = &validation
	}
	if result.Status == "" {
		result.Status = "error"
	}
	result.DurationMs = time.Since(start).Milliseconds()
	return result
}

func buildHarnessOptions(ctx context.Context, opts Options, ir CompiledIR) (recipetest.HarnessOptions, func(), error) {
	deps := coreops.NewServiceDepsBuilder().Build()
	workRoot, err := os.MkdirTemp("", "c2j-test-work-*")
	if err != nil {
		return recipetest.HarnessOptions{}, nil, err
	}
	cleanups := []func(){func() { _ = os.RemoveAll(workRoot) }}

	if suiteUsesPassthrough(ir.Cases) {
		handle, root, err := openDisposableEmbedRuntime(ctx, opts)
		if err != nil {
			for _, cleanup := range cleanups {
				cleanup()
			}
			return recipetest.HarnessOptions{}, nil, err
		}
		ctl := &workerworkflow.SWFWorkflowControl{
			Engine:                        handle.Engine,
			PreferRuntimeRecipeResolution: true,
		}
		deps = coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()
		cleanups = append(cleanups, func() {
			_ = handle.Cleanup()
			if !opts.KeepRuntime && root != "" && strings.TrimSpace(opts.RuntimeRoot) == "" {
				_ = os.RemoveAll(root)
			}
		})
	}

	return recipetest.HarnessOptions{
			Resolver: defaultTargetResolver{},
			Deps:     deps,
			WorkRoot: workRoot,
		}, func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}, nil
}

func suiteUsesPassthrough(cases []recipetest.Case) bool {
	for _, c := range cases {
		for _, mock := range c.Mocks.Ops {
			switch mock.Behavior.Mode {
			case "passthrough", "record_passthrough", "replay":
				return true
			}
		}
	}
	return false
}

func openDisposableEmbedRuntime(ctx context.Context, opts Options) (*swfruntime.Handle, string, error) {
	root := strings.TrimSpace(opts.RuntimeRoot)
	var err error
	if root == "" {
		root, err = os.MkdirTemp("", "c2j-test-embed-*")
		if err != nil {
			return nil, "", err
		}
	} else if !filepath.IsAbs(root) {
		root, err = absPathFromWorkingDir(opts.WorkingDir, root)
		if err != nil {
			return nil, "", err
		}
	}

	embedEnvMu.Lock()
	defer embedEnvMu.Unlock()

	prior, hadPrior := os.LookupEnv(defaults.EmbedRootEnv)
	if err := os.Setenv(defaults.EmbedRootEnv, root); err != nil {
		if strings.TrimSpace(opts.RuntimeRoot) == "" {
			_ = os.RemoveAll(root)
		}
		return nil, "", err
	}
	restore := func() {
		if hadPrior {
			_ = os.Setenv(defaults.EmbedRootEnv, prior)
		} else {
			_ = os.Unsetenv(defaults.EmbedRootEnv)
		}
	}

	handle, err := swfruntime.Open(ctx, defaults.EmbedURL)
	restore()
	if err != nil {
		if strings.TrimSpace(opts.RuntimeRoot) == "" {
			_ = os.RemoveAll(root)
		}
		return nil, "", err
	}
	return handle, root, nil
}

func buildTargetRecipe(ctx context.Context, opts Options) (recipetest.TargetRecipe, error) {
	recipeFile := strings.TrimSpace(opts.RecipeFile)
	if recipeFile == "" && compiler.IsLocalRecipeFileReference(opts.Recipe) {
		recipeFile = strings.TrimSpace(opts.Recipe)
	}
	if recipeFile != "" {
		absPath, err := absPathFromWorkingDir(opts.WorkingDir, recipeFile)
		if err != nil {
			return recipetest.TargetRecipe{}, err
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return recipetest.TargetRecipe{}, err
		}
		format := "yaml"
		if strings.EqualFold(filepath.Ext(absPath), ".json") {
			format = "json"
		}
		target := recipetest.TargetRecipe{Mode: "inline_recipe", Format: format, Content: string(data)}
		expanded, _, err := recipetest.ExpandInlineRecipeTarget(ctx, target, recipetest.InlineTargetExpansionOptions{
			ProjectID:  opts.TenantID,
			RootFile:   absPath,
			WorkingDir: opts.WorkingDir,
			Resolver:   compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{}),
		})
		if err != nil {
			return recipetest.TargetRecipe{}, fmt.Errorf("resolve inline recipes: %w", err)
		}
		return expanded, nil
	}

	selector := strings.TrimSpace(opts.Recipe)
	if selector == "" {
		selector = compiler.DefaultRecipeName
	}
	if !compiler.IsGitRecipeSelector(selector) {
		var err error
		selector, err = buildCurrentCellRecipeSelector(ctx, opts, selector)
		if err != nil {
			return recipetest.TargetRecipe{}, err
		}
	}
	return recipetest.TargetRecipe{Mode: "recipe_selector", Selector: selector}, nil
}

func loadSuiteBytes(opts Options) ([]byte, string, error) {
	if opts.UseStdin {
		b, err := io.ReadAll(bufio.NewReader(opts.Stdin))
		if err != nil {
			return nil, "", err
		}
		return b, resolveSuiteFormat(opts.Format, ""), nil
	}
	absPath, err := absPathFromWorkingDir(opts.WorkingDir, opts.FilePath)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, "", err
	}
	return b, resolveSuiteFormat(opts.Format, absPath), nil
}

func resolveSuiteFormat(explicit string, filePath string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".json":
		return "canonical_json"
	case ".md", ".markdown":
		return "scenario_md"
	case ".yaml", ".yml":
		return "canonical_yaml"
	default:
		return "canonical_yaml"
	}
}

func parseSuiteCases(raw []byte, format string) ([]recipetest.Case, error) {
	var suite suiteEnvelope
	switch format {
	case "canonical_json":
		if err := json.Unmarshal(raw, &suite); err != nil {
			return nil, err
		}
	case "canonical_yaml", "compact_yaml":
		if err := yaml.Unmarshal(raw, &suite); err != nil {
			return nil, err
		}
	case "scenario_md":
		block := extractFencedBlock(string(raw))
		if block == "" {
			return nil, fmt.Errorf("scenario markdown must include a fenced yaml/json block")
		}
		if strings.HasPrefix(strings.TrimSpace(block), "{") {
			if err := json.Unmarshal([]byte(block), &suite); err != nil {
				return nil, err
			}
		} else if err := yaml.Unmarshal([]byte(block), &suite); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	cases := make([]recipetest.Case, 0, len(suite.Cases))
	for _, rawCase := range suite.Cases {
		b, err := json.Marshal(rawCase)
		if err != nil {
			return nil, err
		}
		var c recipetest.Case
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, nil
}

func extractFencedBlock(md string) string {
	lines := strings.Split(md, "\n")
	inBlock := false
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inBlock {
				break
			}
			inBlock = true
			continue
		}
		if inBlock {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func filterCasesByIDs(cases []recipetest.Case, ids []string) []recipetest.Case {
	if len(ids) == 0 {
		return cases
	}
	want := map[string]struct{}{}
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]recipetest.Case, 0, len(cases))
	for _, c := range cases {
		if _, ok := want[c.ID]; ok {
			out = append(out, c)
		}
	}
	return out
}

func writeRunArtifacts(outDir string, results []CaseResult) error {
	summary := map[string]interface{}{"cases": len(results), "results": results}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "summary.json"), b, 0o644); err != nil {
		return err
	}

	md := &strings.Builder{}
	md.WriteString("# Recipe Test Summary\n\n")
	for _, r := range results {
		md.WriteString(fmt.Sprintf("- %s: %s (%dms)\n", r.CaseID, r.Status, r.DurationMs))
	}
	if err := os.WriteFile(filepath.Join(outDir, "summary.md"), []byte(md.String()), 0o644); err != nil {
		return err
	}

	for _, r := range results {
		caseDir := filepath.Join(outDir, "cases", sanitizeCaseID(r.CaseID))
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			return err
		}
		caseJSON, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(caseDir, "result.json"), caseJSON, 0o644); err != nil {
			return err
		}
		if r.Run == nil {
			continue
		}
		if len(r.Run.Evaluations) > 0 {
			evalJSON, err := json.MarshalIndent(r.Run.Evaluations, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(caseDir, "evaluations.json"), evalJSON, 0o644); err != nil {
				return err
			}
		}
		if len(r.Run.Artifacts) == 0 {
			continue
		}
		artDir := filepath.Join(caseDir, "artifacts")
		if err := os.MkdirAll(artDir, 0o755); err != nil {
			return err
		}
		for name, entry := range r.Run.Artifacts {
			if strings.TrimSpace(entry.ContentBase64) == "" {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(entry.ContentBase64)
			if err != nil {
				continue
			}
			full := filepath.Join(artDir, name)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, decoded, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeJSONLEvents(path string, results []CaseResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range results {
		evt := map[string]interface{}{"event": "case_completed", "case_id": r.CaseID, "status": r.Status, "duration_ms": r.DurationMs}
		if err := enc.Encode(evt); err != nil {
			return err
		}
	}
	return enc.Encode(map[string]interface{}{"event": "summary", "cases": len(results)})
}

func countByStatus(results []CaseResult, status string) int {
	n := 0
	for _, r := range results {
		if r.Status == status {
			n++
		}
	}
	return n
}

func isFailureStatus(status string) bool {
	switch status {
	case "invalid", "failed", "error", "timed_out":
		return true
	default:
		return false
	}
}

func sanitizeCaseID(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

func absPathFromWorkingDir(workingDir string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(value) {
		return value, nil
	}
	return filepath.Abs(filepath.Join(workingDir, value))
}

type defaultTargetResolver struct{}

func (defaultTargetResolver) ResolveRecipeTarget(ctx context.Context, tenantID string, target recipetest.TargetRecipe) (*recipe.Recipe, string, []recipetest.Issue, []recipetest.Issue) {
	resolver := compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{})
	resolution, err := resolver.Resolve(ctx, tenantID, target.Selector)
	if err != nil {
		return nil, "", []recipetest.Issue{{Code: "target_not_found", Field: "target_recipe.selector", Message: err.Error()}}, nil
	}

	yamlLoader, ok := resolver.(compiler.RecipeSourceYAMLLoader)
	if !ok {
		return nil, "", []recipetest.Issue{{Code: "target_not_found", Field: "target_recipe.selector", Message: "recipe source resolver cannot load YAML"}}, nil
	}
	yamlBytes, err := yamlLoader.LoadYAML(ctx, tenantID, resolution)
	if err != nil {
		return nil, "", []recipetest.Issue{{Code: "target_not_found", Field: "target_recipe.selector", Message: err.Error()}}, nil
	}
	rec, err := recipe.LoadRecipeFromString(yamlBytes)
	if err != nil {
		return nil, "", []recipetest.Issue{{Code: "invalid_recipe", Field: "target_recipe.selector", Message: err.Error()}}, nil
	}
	expanded, err := compiler.ResolveInlineRecipes(ctx, *rec, compiler.InlineResolutionOptions{
		ProjectID:  tenantID,
		Resolver:   resolver,
		RootSource: &resolution,
	})
	if err != nil {
		return nil, "", []recipetest.Issue{{Code: "invalid_recipe", Field: "target_recipe.selector", Message: err.Error()}}, nil
	}
	snapshot, err := yaml.Marshal(&expanded.Recipe)
	if err != nil {
		return nil, "", []recipetest.Issue{{Code: "invalid_recipe", Field: "target_recipe.selector", Message: err.Error()}}, nil
	}
	hash := sha256.Sum256(snapshot)
	return &expanded.Recipe, hex.EncodeToString(hash[:]), nil, nil
}

func buildCurrentCellRecipeSelector(ctx context.Context, opts Options, recipeName string) (string, error) {
	repo, ref, err := resolveTargetRepo(ctx, opts)
	if err != nil {
		return "", err
	}
	return compiler.BuildCellRecipeSelector(repo, recipeName, ref)
}

func resolveTargetRepo(ctx context.Context, opts Options) (string, string, error) {
	cfg, cfgErr := configpkg.LoadProjectConfig(opts.WorkingDir)
	if cfgErr != nil && cfgErr != configpkg.ErrConfigNotFound {
		return "", "", cfgErr
	}

	if opts.Self || strings.TrimSpace(opts.Cell) == "" {
		if cfgErr == configpkg.ErrConfigNotFound || cfg == nil {
			return "", "", fmt.Errorf("current cell requires .c2j/config.yaml or supported auto-detection")
		}
		repo, err := cfg.SelfRepo(ctx)
		if err != nil {
			return "", "", err
		}
		ref, err := cfg.SelfRef(ctx)
		if err != nil {
			return "", "", err
		}
		return repo, ref, nil
	}

	cell := strings.TrimSpace(opts.Cell)
	repo := cell
	var err error
	if cfg != nil {
		repo, err = cfg.ExpandCellName(ctx, cell)
		if err != nil {
			return "", "", err
		}
	} else if filepath.IsAbs(cell) || strings.HasPrefix(cell, "./") || strings.HasPrefix(cell, "../") {
		repo, err = absPathFromWorkingDir(opts.WorkingDir, cell)
		if err != nil {
			return "", "", err
		}
	}
	ref := compiler.DefaultRecipeRef
	if cfg != nil {
		if selfRepo, err := cfg.SelfRepo(ctx); err == nil {
			selfSource, selfErr := compiler.NormalizeGitRepositorySource(selfRepo)
			repoSource, repoErr := compiler.NormalizeGitRepositorySource(repo)
			if selfErr == nil && repoErr == nil && selfSource == repoSource {
				ref, _ = cfg.SelfRef(ctx)
			}
		}
	}
	return repo, ref, nil
}
