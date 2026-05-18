package recipetest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipecore "github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/redact"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/go-playground/validator/v10"
)

var validatorInstance = validator.New()

type caseInput struct {
	TargetRecipe TargetRecipe     `json:"target_recipe" validate:"required"`
	Case         Case             `json:"case" validate:"required"`
	Execution    ExecutionOptions `json:"execution,omitempty"`
}

type TargetRecipe struct {
	Mode     string `json:"mode" validate:"required,oneof=inline_recipe recipe_selector"`
	Selector string `json:"selector,omitempty"`
	Format   string `json:"format,omitempty" validate:"omitempty,oneof=yaml json"`
	Content  string `json:"content,omitempty"`
}

type Case struct {
	ID          string                 `json:"id" validate:"required"`
	Type        string                 `json:"type" validate:"required,oneof=op_case recipe_case integration_case"`
	Target      map[string]interface{} `json:"target,omitempty"`
	Inputs      map[string]interface{} `json:"inputs,omitempty"`
	Mocks       Mocks                  `json:"mocks,omitempty"`
	Assertions  []Assertion            `json:"assertions,omitempty"`
	Evaluations []Evaluation           `json:"evaluations,omitempty"`
	Options     map[string]interface{} `json:"options,omitempty"`
}

type Mocks struct {
	Ops []OpMock `json:"ops,omitempty" validate:"omitempty,dive"`
}

type OpMock struct {
	Match    OpMockMatch  `json:"match" validate:"required"`
	Behavior MockBehavior `json:"behavior" validate:"required"`
}

type OpMockMatch struct {
	NodePath string `json:"node_path,omitempty"`
	Op       string `json:"op,omitempty"`
}

type MockBehavior struct {
	Mode        string            `json:"mode" validate:"required,oneof=return fail passthrough record_passthrough replay"`
	Outputs     map[string]any    `json:"outputs,omitempty"`
	Artifacts   map[string]string `json:"artifacts,omitempty"`
	Error       *TestError        `json:"error,omitempty"`
	CassetteKey string            `json:"cassette_key,omitempty"`
}

type TestError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Assertion struct {
	Type      string      `json:"type" validate:"required,oneof=output_equals output_matches artifact_exists artifact_json_equals node_executed node_not_executed status_is cel_true var_equals transition_payload_equals"`
	Path      string      `json:"path,omitempty"`
	Value     interface{} `json:"value,omitempty"`
	Regex     string      `json:"regex,omitempty"`
	JsonPath  string      `json:"json_path,omitempty"`
	NodePath  string      `json:"node_path,omitempty"`
	Scope     string      `json:"scope,omitempty"`
	FromState string      `json:"from_state,omitempty"`
	ToState   string      `json:"to_state,omitempty"`
	Status    string      `json:"status,omitempty"`
	Expr      string      `json:"expr,omitempty"`
}

type Evaluation struct {
	ID     string                 `json:"id" validate:"required"`
	Type   string                 `json:"type" validate:"required,oneof=text_pattern llm_judge"`
	Mode   string                 `json:"mode,omitempty" validate:"omitempty,oneof=enforce report_only"`
	Source EvalSource             `json:"source" validate:"required"`
	Config map[string]interface{} `json:"config,omitempty"`
}

type EvalSource struct {
	Kind           string `json:"kind" validate:"required,oneof=artifact artifact_glob output_path trace"`
	Path           string `json:"path,omitempty"`
	Glob           string `json:"glob,omitempty"`
	OutputPath     string `json:"output_path,omitempty"`
	TraceName      string `json:"trace_name,omitempty"`
	AllowSensitive bool   `json:"allow_sensitive,omitempty"`
}

type ExecutionOptions struct {
	Mode             string `json:"mode,omitempty" validate:"omitempty,oneof=isolated"`
	Timeout          string `json:"timeout,omitempty"`
	ArtifactMode     string `json:"artifact_mode,omitempty" validate:"omitempty,oneof=none inline"`
	ArtifactMaxBytes int64  `json:"artifact_max_bytes,omitempty" validate:"omitempty,gte=1"`
	EvaluationMode   string `json:"evaluation_mode,omitempty" validate:"omitempty,oneof=enforce report_only"`
}

type ValidationResult struct {
	Valid    bool    `json:"valid"`
	CaseHash string  `json:"case_hash"`
	Errors   []Issue `json:"errors,omitempty"`
	Warnings []Issue `json:"warnings,omitempty"`
}

type Issue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type CaseRunResult struct {
	CaseId          string                    `json:"case_id"`
	Status          string                    `json:"status"`
	CaseHash        string                    `json:"case_hash"`
	DurationMs      int64                     `json:"duration_ms"`
	FailureCategory string                    `json:"failure_category,omitempty"`
	FailureReason   string                    `json:"failure_reason,omitempty"`
	Outputs         map[string]interface{}    `json:"outputs,omitempty"`
	Assertions      []AssertionResult         `json:"assertions,omitempty"`
	Evaluations     []EvaluationResult        `json:"evaluations,omitempty"`
	Diagnostics     Diagnostics               `json:"diagnostics,omitempty"`
	Artifacts       map[string]InlineArtifact `json:"artifacts,omitempty"`
}

type AssertionResult struct {
	Type     string      `json:"type"`
	Passed   bool        `json:"passed"`
	Message  string      `json:"message,omitempty"`
	Expected interface{} `json:"expected,omitempty"`
	Actual   interface{} `json:"actual,omitempty"`
}

type EvaluationResult struct {
	Id            string                 `json:"id"`
	Type          string                 `json:"type"`
	Mode          string                 `json:"mode"`
	Passed        bool                   `json:"passed"`
	ErrorCategory string                 `json:"error_category,omitempty"`
	ErrorMessage  string                 `json:"error_message,omitempty"`
	Output        map[string]interface{} `json:"output,omitempty"`
}

type InlineArtifact struct {
	ContentBase64 string `json:"content_base64"`
	SizeBytes     int64  `json:"size_bytes"`
	Truncated     bool   `json:"truncated"`
}

type Diagnostics struct {
	MockHits    []MockHit                `json:"mock_hits,omitempty"`
	MockMisses  []MockMiss               `json:"mock_misses,omitempty"`
	Vars        []RenderedVarsDiagnostic `json:"vars,omitempty"`
	Transitions []TransitionDiagnostic   `json:"transitions,omitempty"`
}

type RenderedVarsDiagnostic struct {
	NodePath string                 `json:"node_path"`
	Scope    string                 `json:"scope"`
	Vars     map[string]interface{} `json:"vars"`
}

type TransitionDiagnostic struct {
	FromState  string                 `json:"from_state,omitempty"`
	ToState    string                 `json:"to_state,omitempty"`
	Expression string                 `json:"expression,omitempty"`
	Result     bool                   `json:"result"`
	Selected   bool                   `json:"selected,omitempty"`
	Payload    map[string]interface{} `json:"payload,omitempty"`
}

type MockHit struct {
	NodePath string `json:"node_path"`
	Op       string `json:"op"`
	Mode     string `json:"mode"`
}

type MockMiss struct {
	NodePath string `json:"node_path"`
	Op       string `json:"op"`
	Reason   string `json:"reason"`
}

type HarnessOptions struct {
	Resolver   TargetResolver
	Deps       coreops.ServiceDependencies2
	CELOptions template.CELOptionsProvider
	WorkRoot   string
}

type TargetResolver interface {
	ResolveRecipeTarget(ctx context.Context, tenantID string, target TargetRecipe) (*recipecore.Recipe, string, []Issue, []Issue)
}

type preparedCase struct {
	Recipe       *recipecore.Recipe
	ResolvedHash string
	Validation   ValidationResult
}

func ValidateCase(ctx context.Context, opts HarnessOptions, tenantID string, target TargetRecipe, c Case) ValidationResult {
	opts = opts.withDefaults()
	input := caseInput{TargetRecipe: target, Case: c}
	prepared := prepareCase(ctx, opts, tenantID, input)
	if prepared.Recipe != nil && len(prepared.Validation.Errors) == 0 {
		prepared.Validation.Errors = append(prepared.Validation.Errors, validateRecipeExecutionSemantics(ctx, opts, tenantID, input, prepared.Recipe, prepared.ResolvedHash)...)
		prepared.Validation.Valid = len(prepared.Validation.Errors) == 0
	}
	return prepared.Validation
}

func RunCase(ctx context.Context, opts HarnessOptions, tenantID string, target TargetRecipe, c Case, exec ExecutionOptions) CaseRunResult {
	opts = opts.withDefaults()
	input := caseInput{TargetRecipe: target, Case: c, Execution: exec}
	prepared := prepareCase(ctx, opts, tenantID, input)
	if len(prepared.Validation.Errors) > 0 {
		return CaseRunResult{
			CaseId:          c.ID,
			Status:          "invalid",
			CaseHash:        prepared.Validation.CaseHash,
			FailureCategory: "validation_error",
			FailureReason:   issueSummary(prepared.Validation.Errors),
		}
	}
	return runPreparedCase(ctx, opts, tenantID, input, prepared)
}

func (o HarnessOptions) withDefaults() HarnessOptions {
	if o.Deps == nil {
		o.Deps = coreops.NewServiceDepsBuilder().Build()
	}
	return o
}

func issueSummary(issues []Issue) string {
	if len(issues) == 0 {
		return ""
	}
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		if strings.TrimSpace(issue.Field) != "" {
			messages = append(messages, issue.Field+": "+issue.Message)
			continue
		}
		messages = append(messages, issue.Message)
	}
	return strings.Join(messages, "; ")
}

func validationIssues(err error) []Issue {
	out := make([]Issue, 0)
	if verrs, ok := err.(validator.ValidationErrors); ok {
		for _, fe := range verrs {
			out = append(out, Issue{Code: "validation_error", Field: fe.Namespace(), Message: fe.Error()})
		}
		return out
	}
	return []Issue{{Code: "validation_error", Message: err.Error()}}
}

func validateRecipeSemantics(req caseInput) []Issue {
	issues := make([]Issue, 0)
	target := req.TargetRecipe
	switch target.Mode {
	case "inline_recipe":
		if strings.TrimSpace(target.Content) == "" {
			issues = append(issues, Issue{Code: "invalid_target", Field: "target_recipe.content", Message: "inline_recipe requires content"})
		}
	case "recipe_selector":
		if strings.TrimSpace(target.Selector) == "" {
			issues = append(issues, Issue{Code: "invalid_target", Field: "target_recipe.selector", Message: "recipe_selector requires selector"})
		}
	default:
		issues = append(issues, Issue{Code: "invalid_target", Field: "target_recipe.mode", Message: "unsupported target mode: " + target.Mode})
	}
	if req.Case.Type == "op_case" {
		nodePath, _ := req.Case.Target["node_path"].(string)
		if strings.TrimSpace(nodePath) == "" {
			issues = append(issues, Issue{Code: "invalid_case", Field: "case.target.node_path", Message: "op_case requires target.node_path"})
		}
	}
	for i, m := range req.Case.Mocks.Ops {
		if strings.TrimSpace(m.Match.NodePath) == "" && strings.TrimSpace(m.Match.Op) == "" {
			issues = append(issues, Issue{Code: "invalid_mock", Field: fmt.Sprintf("case.mocks.ops[%d].match", i), Message: "at least one matcher field is required"})
		}
		if m.Behavior.Mode == "replay" && strings.TrimSpace(m.Behavior.CassetteKey) == "" {
			issues = append(issues, Issue{Code: "invalid_mock", Field: fmt.Sprintf("case.mocks.ops[%d].behavior.cassette_key", i), Message: "replay mode requires cassette_key"})
		}
	}
	if req.Case.Type == "op_case" {
		if _, ok := parseOpCaseTarget(req.Case); !ok {
			issues = append(issues, Issue{Code: "invalid_case", Field: "case.target.node_path", Message: "op_case requires non-empty target.node_path"})
		}
	}
	if req.Case.Options != nil {
		policyOpts := getMap(req.Case.Options, "policy")
		for _, dep := range stringSlice(policyOpts["required_dependencies"]) {
			if !isSupportedDependencyName(dep) {
				issues = append(issues, Issue{Code: "invalid_option", Field: "case.options.policy.required_dependencies", Message: "unsupported dependency name: " + dep})
			}
		}
	}
	return issues
}

func prepareCase(ctx context.Context, opts HarnessOptions, tenantID string, req caseInput) preparedCase {
	validate := ValidationResult{Valid: false}
	if err := validatorInstance.Struct(req); err != nil {
		validate.Errors = append(validate.Errors, validationIssues(err)...)
	}
	recipeDef, recipeHash, errs, warns := resolveRecipeTestTarget(ctx, opts, tenantID, req.TargetRecipe)
	caseHash := computeCaseHash(recipeHash, req.Case)
	validate.CaseHash = caseHash
	validate.Errors = append(errs, validateRecipeSemantics(req)...)
	validate.Errors = append(validate.Errors, validateDependencyAvailability(opts, req)...)
	validate.Warnings = warns
	validate.Valid = len(validate.Errors) == 0
	return preparedCase{Recipe: recipeDef, ResolvedHash: recipeHash, Validation: validate}
}

func validateRecipeExecutionSemantics(ctx context.Context, opts HarnessOptions, tenantID string, req caseInput, recipeDef *recipecore.Recipe, recipeHash string) []Issue {
	rawInputs := req.Case.Inputs
	if rawInputs == nil {
		rawInputs = map[string]interface{}{}
	}
	jobCtx := newTestJobContext(tenantID, req.Case, TestPolicy{}, opts.Deps)
	wfCtx := workflow.Context{JobContext: jobCtx, ServiceDependencies2: opts.Deps}
	runCtx := contextual.JobContext{
		Environment: contextual.EnvironmentContext{WorktreePath: contextual.WorktreePathSentinel, WorkdirPath: contextual.WorkdirPathSentinel, ArtifactInbox: contextual.ArtifactInboxSentinel, ArtifactOutbox: contextual.ArtifactOutboxSentinel},
		Workflow:    contextual.WorkflowContext{CellName: "recipe-tests", ProjectId: tenantID},
		GitBase:     contextual.GitBaseContext{BaseRepo: "recipe-tests", BaseRef: recipeHash, ResolvedBaseHash: recipeHash},
	}
	gitCtx := contextual.GitCommitContext{ParentRef: recipeHash}
	_ = ctx
	_, _, err := compiler.ExecuteRecipe(wfCtx, *recipeDef, rawInputs, runCtx, gitCtx, compiler.ExecutionOptions{
		Mode:                compiler.ExecutionModeValidate,
		Validation:          compiler.ValidationOptions{Mode: compiler.ValidateAll, CollectAll: true},
		CELOptionsProvider:  opts.CELOptions,
		StateObserver:       jobCtx,
		DiagnosticsObserver: jobCtx,
	})
	if err == nil {
		return nil
	}
	return []Issue{{Code: "semantic_validation", Field: "target_recipe", Message: err.Error()}}
}

func resolveRecipeTestTarget(ctx context.Context, opts HarnessOptions, tenantID string, target TargetRecipe) (*recipecore.Recipe, string, []Issue, []Issue) {
	errorsList := make([]Issue, 0)
	warnings := make([]Issue, 0)
	if target.Mode == "recipe_selector" {
		if opts.Resolver == nil {
			return nil, "", []Issue{{Code: "resolver_unavailable", Message: "recipe selector target requires a resolver"}}, warnings
		}
		return opts.Resolver.ResolveRecipeTarget(ctx, tenantID, target)
	}

	content := []byte(target.Content)
	if target.Format == "json" && !json.Valid(content) {
		return nil, "", []Issue{{Code: "invalid_target", Field: "target_recipe.content", Message: "invalid json content"}}, warnings
	}
	recipeDef, dynamicWarnings, err := loadRecipeWithDynamicOpStubs(content)
	warnings = append(warnings, dynamicWarnings...)
	if err != nil {
		return nil, "", []Issue{{Code: "invalid_recipe", Field: "target_recipe.content", Message: err.Error()}}, warnings
	}
	hash := sha256.Sum256(content)
	return recipeDef, hex.EncodeToString(hash[:]), errorsList, warnings
}

func validateDependencyAvailability(opts HarnessOptions, req caseInput) []Issue {
	issues := make([]Issue, 0)
	if !caseUsesPassthrough(req) {
		return issues
	}
	if opts.Deps == nil {
		issues = append(issues, Issue{Code: "dependency_unavailable", Field: "case.mocks.ops", Message: "passthrough mock mode requires runtime dependencies"})
		return issues
	}
	required := normalizedExecutionConfig(req).Policy.RequiredDependencies
	for dep := range required {
		if !dependencyAvailable(opts.Deps, dep) {
			issues = append(issues, Issue{Code: "dependency_unavailable", Field: "case.options.policy.required_dependencies", Message: "required dependency unavailable: " + dep})
		}
	}
	return issues
}

func dependencyAvailable(deps coreops.ServiceDependencies2, name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "database":
		return deps != nil && deps.Database() != nil
	case "workflow_control":
		return deps != nil && deps.WorkflowControl() != nil
	case "sse_manager":
		return deps != nil && deps.SSEManager() != nil
	default:
		return false
	}
}

func caseUsesPassthrough(req caseInput) bool {
	for _, m := range req.Case.Mocks.Ops {
		switch m.Behavior.Mode {
		case "passthrough", "record_passthrough", "replay":
			return true
		}
	}
	return false
}

func runPreparedCase(ctx context.Context, opts HarnessOptions, tenantID string, req caseInput, prepared preparedCase) CaseRunResult {
	started := time.Now()
	execResp := CaseRunResult{CaseId: req.Case.ID, Status: "passed", CaseHash: prepared.Validation.CaseHash}
	timeout := parseTimeout(req.Execution.Timeout, 60*time.Second)
	recipeDef := withRecipeTimeoutOverlay(prepared.Recipe, timeout)

	execCfg := normalizedExecutionConfig(req)
	jobCtx := newTestJobContext(tenantID, req.Case, execCfg.Policy, opts.Deps)
	wfCtx := workflow.Context{JobContext: jobCtx, ServiceDependencies2: opts.Deps}
	rawInputs := req.Case.Inputs
	if rawInputs == nil {
		rawInputs = map[string]interface{}{}
	}
	workRoot := strings.TrimSpace(opts.WorkRoot)
	if workRoot == "" {
		workRoot = filepath.Join(os.TempDir(), "c2j-recipe-tests")
	}
	operationPaths := recipeTestOperationPaths(workRoot)
	if err := ensureRecipeTestOperationDirs(operationPaths); err != nil {
		execResp.Status = "failed"
		execResp.FailureCategory = "runtime_error"
		execResp.FailureReason = err.Error()
		return execResp
	}
	jobCtx.operationPaths = operationPaths
	runCtx := contextual.JobContext{
		Environment: recipeTestEnvironment(operationPaths),
		Workflow:    contextual.WorkflowContext{CellName: "recipe-tests", ProjectId: tenantID},
		GitBase:     contextual.GitBaseContext{BaseRepo: "recipe-tests", BaseRef: prepared.ResolvedHash, ResolvedBaseHash: prepared.ResolvedHash},
	}
	gitCtx := contextual.GitCommitContext{ParentRef: prepared.ResolvedHash}
	timedOut := false

	outputs, artifacts, err := compiler.ExecuteRecipe(
		wfCtx,
		recipeDef,
		rawInputs,
		runCtx,
		gitCtx,
		compiler.ExecutionOptions{CELOptionsProvider: opts.CELOptions, StateObserver: jobCtx, DiagnosticsObserver: jobCtx},
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			timedOut = true
			execResp.Status = "timed_out"
			execResp.FailureCategory = "timeout"
			execResp.FailureReason = "execution timed out"
		} else {
			execResp.Status = "failed"
			execResp.FailureCategory = failureCategoryFromError(err)
			execResp.FailureReason = err.Error()
		}
	} else {
		execResp.Outputs = outputs
	}

	artifactBytes := collectArtifactBytes(ctx, jobCtx, artifacts)
	assertionResults, assertionFailed := runRecipeTestAssertions(req.Case.Assertions, execResp.Outputs, artifactBytes, jobCtx.executedNodes, execResp.Status, jobCtx.vars, jobCtx.transitions)
	execResp.Assertions = assertionResults
	if assertionFailed {
		markFailure(&execResp, "assertion_failure", "one or more assertions failed")
	}

	evalResults, evalFailed := runRecipeTestEvaluations(req.Case.Evaluations, execResp.Outputs, artifactBytes, execCfg.EvaluationMode)
	execResp.Evaluations = evalResults
	if evalFailed {
		markFailure(&execResp, "evaluation_failure", "one or more enforced evaluations failed")
	}

	execResp.Diagnostics = Diagnostics{MockHits: jobCtx.mockHits, MockMisses: jobCtx.mockMisses, Vars: redactVarsDiagnostics(jobCtx.vars), Transitions: redactTransitionDiagnostics(jobCtx.transitions)}
	if execCfg.ArtifactMode == "inline" {
		execResp.Artifacts = inlineArtifacts(artifactBytes, execCfg.ArtifactMaxBytes)
	}
	if execCfg.Policy.AllowedOnlyNodePath != "" && !jobCtx.executedNodes[execCfg.Policy.AllowedOnlyNodePath] {
		markFailure(&execResp, "runtime_error", "target op_case node was not executed")
	}

	if timedOut {
		execResp.Status = "timed_out"
		execResp.FailureCategory = "timeout"
		execResp.FailureReason = "execution timed out"
	}
	execResp.DurationMs = time.Since(started).Milliseconds()
	return execResp
}

func withRecipeTimeoutOverlay(rec *recipecore.Recipe, timeout time.Duration) recipecore.Recipe {
	if rec == nil || rec.RecipeImpl == nil || timeout <= 0 {
		if rec == nil {
			return recipecore.Recipe{}
		}
		return *rec
	}
	apply := func(metadata recipecore.RecipeMetadata) recipecore.RecipeMetadata {
		metadata.NodeMetadata.Timeout = overlayTimeout(metadata.NodeMetadata.Timeout, timeout)
		return metadata
	}
	switch typed := rec.RecipeImpl.(type) {
	case *recipecore.RecipeOp:
		clone := *typed
		clone.RecipeMetadata = apply(typed.RecipeMetadata)
		return recipecore.Recipe{RecipeImpl: &clone}
	case *recipecore.RecipeSequence:
		clone := *typed
		clone.RecipeMetadata = apply(typed.RecipeMetadata)
		return recipecore.Recipe{RecipeImpl: &clone}
	case *recipecore.RecipeState:
		clone := *typed
		clone.RecipeMetadata = apply(typed.RecipeMetadata)
		return recipecore.Recipe{RecipeImpl: &clone}
	default:
		return *rec
	}
}

func overlayTimeout(existing recipecore.Duration, overlay time.Duration) recipecore.Duration {
	current := time.Duration(existing)
	if current <= 0 || overlay < current {
		return recipecore.Duration(overlay)
	}
	return existing
}

func recipeTestOperationPaths(workRoot string) coreops.OperationPaths {
	workdir := filepath.Join(workRoot, "workdir")
	return coreops.OperationPaths{
		Workdir:      workdir,
		WorktreePath: filepath.Join(workdir, "worktree"),
		Inbox:        filepath.Join(workdir, "inbox"),
		Outbox:       filepath.Join(workdir, "outbox"),
	}
}

func recipeTestEnvironment(paths coreops.OperationPaths) contextual.EnvironmentContext {
	return contextual.EnvironmentContext{
		WorktreePath:   paths.WorktreePath,
		WorkdirPath:    paths.Workdir,
		ArtifactInbox:  paths.Inbox,
		ArtifactOutbox: paths.Outbox,
		Host: contextual.EnvironmentPathContext{
			Workdir:      paths.Workdir,
			WorktreePath: paths.WorktreePath,
			Inbox:        paths.Inbox,
			Outbox:       paths.Outbox,
		},
		Op: contextual.EnvironmentPathContext{
			Workdir:      paths.Workdir,
			WorktreePath: paths.WorktreePath,
			Inbox:        paths.Inbox,
			Outbox:       paths.Outbox,
		},
	}
}

func ensureRecipeTestOperationDirs(paths coreops.OperationPaths) error {
	for _, dir := range []string{paths.Workdir, paths.WorktreePath, paths.Inbox, paths.Outbox} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create recipe test op directory %q: %w", dir, err)
		}
	}
	return nil
}

var unknownOpPattern = regexp.MustCompile(`unknown op: \[([^\]]+)\]`)

func loadRecipeWithDynamicOpStubs(content []byte) (*recipecore.Recipe, []Issue, error) {
	warnings := make([]Issue, 0)
	for i := 0; i < 32; i++ {
		r, err := recipecore.LoadRecipeFromString(content)
		if err == nil {
			return r, warnings, nil
		}
		m := unknownOpPattern.FindStringSubmatch(err.Error())
		if len(m) != 2 {
			return nil, warnings, err
		}
		opName := strings.TrimSpace(m[1])
		if opName == "" {
			return nil, warnings, err
		}
		registerRecipeTestStubOp(opName)
		warnings = append(warnings, Issue{Code: "stubbed_unknown_op", Message: fmt.Sprintf("registered dynamic test stub for op %q", opName)})
	}
	return nil, warnings, fmt.Errorf("too many unknown ops while loading recipe")
}

func registerRecipeTestStubOp(opName string) {
	if _, exists := coreops.Get(opName); exists {
		return
	}
	op := coreops.NewActivityMappedOpV2[map[string]interface{}, map[string]interface{}](coreops.OpMetadata{Type: opName},
		func(_ coreops.OpDependencies, _ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		})
	coreops.Register(op)
}

func computeCaseHash(recipeHash string, c Case) string {
	payload := map[string]interface{}{"recipe_hash": recipeHash, "case": c}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func getMap(root map[string]interface{}, key string) map[string]interface{} {
	if root == nil {
		return nil
	}
	v, ok := root[key]
	if !ok {
		return nil
	}
	m, _ := v.(map[string]interface{})
	return m
}

func stringSlice(v interface{}) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func parseOpCaseTarget(c Case) (string, bool) {
	if c.Type != "op_case" || c.Target == nil {
		return "", false
	}
	nodePath, _ := c.Target["node_path"].(string)
	nodePath = strings.TrimSpace(nodePath)
	return nodePath, nodePath != ""
}

func isSupportedDependencyName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "database", "workflow_control", "sse_manager":
		return true
	default:
		return false
	}
}

type RuntimeConfig struct {
	Policy           TestPolicy
	ArtifactMode     string
	ArtifactMaxBytes int64
	EvaluationMode   string
}

type TestPolicy struct {
	Mode                 string
	RequireMocks         bool
	BlockedOps           map[string]struct{}
	AllowedOnlyNodePath  string
	RequiredDependencies map[string]struct{}
}

func normalizedExecutionConfig(req caseInput) RuntimeConfig {
	mode := req.Execution.Mode
	if mode == "" {
		mode = "isolated"
	}
	artifactMode := req.Execution.ArtifactMode
	if artifactMode == "" {
		artifactMode = "none"
	}
	artifactMax := req.Execution.ArtifactMaxBytes
	if artifactMax <= 0 {
		artifactMax = 64 * 1024
	}
	evalMode := req.Execution.EvaluationMode
	policy := TestPolicy{
		Mode:                 mode,
		RequireMocks:         mode == "isolated",
		BlockedOps:           map[string]struct{}{},
		RequiredDependencies: map[string]struct{}{},
	}

	if options, ok := req.Case.Options["policy"].(map[string]interface{}); ok {
		if v, ok := options["require_mocks"].(bool); ok {
			policy.RequireMocks = v
		}
		if arr, ok := options["blocked_ops"].([]interface{}); ok {
			for _, item := range arr {
				s, _ := item.(string)
				s = strings.TrimSpace(s)
				if s != "" {
					policy.BlockedOps[s] = struct{}{}
				}
			}
		}
		for _, dep := range stringSlice(options["required_dependencies"]) {
			if dep = strings.TrimSpace(dep); dep != "" {
				policy.RequiredDependencies[dep] = struct{}{}
			}
		}
	}

	if req.Case.Type == "op_case" {
		if targetNode, ok := parseOpCaseTarget(req.Case); ok {
			policy.AllowedOnlyNodePath = targetNode
		}
	}

	return RuntimeConfig{Policy: policy, ArtifactMode: artifactMode, ArtifactMaxBytes: artifactMax, EvaluationMode: evalMode}
}

type testJobContext struct {
	jobKey           swf.JobKey
	caseDef          Case
	policy           TestPolicy
	deps             coreops.ServiceDependencies2
	mockHits         []MockHit
	mockMisses       []MockMiss
	mockSelections   map[string]int
	consumedMockIdxs map[int]struct{}
	executedNodes    map[string]bool
	operationPaths   coreops.OperationPaths
	artifactContents map[string][]byte
	artifactOrdinal  int64
	recordings       map[string]passthroughRecord
	vars             []RenderedVarsDiagnostic
	transitions      []TransitionDiagnostic
}

type passthroughRecord struct {
	Outputs   map[string]interface{}
	NextTask  string
	Artifacts map[string][]byte
}

func newTestJobContext(projectID string, caseDef Case, policy TestPolicy, deps coreops.ServiceDependencies2) *testJobContext {
	tenantID := strings.TrimSpace(projectID)
	if tenantID == "" {
		tenantID = "recipe-tests"
	}
	return &testJobContext{
		jobKey:           swf.JobKey{TenantId: tenantID, JobId: "recipe-test-job"},
		caseDef:          caseDef,
		policy:           policy,
		deps:             deps,
		mockSelections:   map[string]int{},
		consumedMockIdxs: map[int]struct{}{},
		executedNodes:    map[string]bool{},
		artifactContents: map[string][]byte{},
		recordings:       map[string]passthroughRecord{},
	}
}

func (j *testJobContext) AwaitJobs(_ ...string) error        { return nil }
func (j *testJobContext) GetJobKey() swf.JobKey              { return j.jobKey }
func (j *testJobContext) Logger() *slog.Logger               { return nil }
func (j *testJobContext) AwaitDuration(_ swf.Duration) error { return nil }

func (j *testJobContext) VarsResolved(scope template.ScopeType, nodePath string, vars map[string]interface{}) {
	j.vars = append(j.vars, RenderedVarsDiagnostic{
		NodePath: strings.TrimSpace(nodePath),
		Scope:    string(scope),
		Vars:     cloneStringMap(vars),
	})
}

func (j *testJobContext) StateEntered(string) {}
func (j *testJobContext) StateExited(string)  {}

func (j *testJobContext) TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string) {
	j.transitions = append(j.transitions, TransitionDiagnostic{
		ToState:    strings.TrimSpace(nextStateIfExpressionTrue),
		Expression: strings.TrimSpace(expression),
		Result:     result,
	})
}

func (j *testJobContext) TransitionSelected(fromState string, toState string, payload map[string]interface{}) {
	selected := TransitionDiagnostic{
		FromState: strings.TrimSpace(fromState),
		ToState:   strings.TrimSpace(toState),
		Result:    true,
		Selected:  true,
		Payload:   cloneStringMap(payload),
	}
	for i := len(j.transitions) - 1; i >= 0; i-- {
		if j.transitions[i].Selected {
			continue
		}
		if strings.TrimSpace(j.transitions[i].ToState) != selected.ToState {
			continue
		}
		j.transitions[i].FromState = selected.FromState
		j.transitions[i].Selected = true
		j.transitions[i].Payload = selected.Payload
		return
	}
	j.transitions = append(j.transitions, selected)
}

func (j *testJobContext) DoTask(runPolicy swf.RunPolicy, taskType string, data swf.TaskData) (swf.TaskData, error) {
	out, _, err := j.doMockedTask(runPolicy, taskType, data, true)
	return out, err
}

func (j *testJobContext) DoValidationTask(runPolicy swf.RunPolicy, taskType string, data swf.TaskData) (swf.TaskData, bool, error) {
	return j.doMockedTask(runPolicy, taskType, data, false)
}

func (j *testJobContext) doMockedTask(runPolicy swf.RunPolicy, taskType string, data swf.TaskData, requireMock bool) (swf.TaskData, bool, error) {
	ctx, cancel := contextForRunPolicy(runPolicy)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, true, err
	}
	raw, err := data.GetData()
	if err != nil {
		return nil, false, err
	}
	var inv workerops.ActivityInvocationRequest
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, false, err
	}
	opName := taskType
	if idx := strings.Index(opName, ":"); idx >= 0 {
		opName = opName[:idx]
	}
	nodePath := strings.TrimSpace(inv.GitTaskContext.NodePath)
	j.executedNodes[nodePath] = true

	if j.policy.AllowedOnlyNodePath != "" && nodePath != j.policy.AllowedOnlyNodePath {
		reason := "node outside op_case target scope"
		j.mockMisses = append(j.mockMisses, MockMiss{NodePath: nodePath, Op: opName, Reason: reason})
		return nil, true, fmt.Errorf("%s: %s", opName, reason)
	}

	invocationKey := fmt.Sprintf("%s::%s::%d", nodePath, opName, inv.GitTaskContext.InvokeSeq)
	idx, matched := j.selectOpMockIndexForInvocation(invocationKey, nodePath, opName)
	if !matched {
		if !requireMock {
			return nil, false, nil
		}
		reason := "unmocked op"
		if hasOpMockCandidate(j.caseDef.Mocks.Ops, nodePath, opName) {
			reason = "mock exhausted for repeated invocation"
		} else if _, blocked := j.policy.BlockedOps[opName]; blocked {
			reason = "blocked by policy"
		} else if j.policy.RequireMocks {
			reason = "policy requires op mock"
		}
		j.mockMisses = append(j.mockMisses, MockMiss{NodePath: nodePath, Op: opName, Reason: reason})
		return nil, true, fmt.Errorf("%s: %s", opName, reason)
	}
	mock := j.caseDef.Mocks.Ops[idx]
	if !requireMock && mock.Behavior.Mode != "return" {
		return nil, false, nil
	}
	j.mockSelections[invocationKey] = idx
	j.consumedMockIdxs[idx] = struct{}{}

	j.mockHits = append(j.mockHits, MockHit{NodePath: nodePath, Op: opName, Mode: mock.Behavior.Mode})
	switch mock.Behavior.Mode {
	case "return":
		out, err := j.buildTaskData(mock.Behavior.Outputs, mock.Behavior.Artifacts, "")
		return out, true, err
	case "fail":
		msg := "mock failure"
		if mock.Behavior.Error != nil && strings.TrimSpace(mock.Behavior.Error.Message) != "" {
			msg = mock.Behavior.Error.Message
		}
		return nil, true, fmt.Errorf("%s", msg)
	case "passthrough", "record_passthrough":
		record, err := j.runPassthroughTask(ctx, taskType, inv)
		if err != nil {
			return nil, true, err
		}
		if mock.Behavior.Mode == "record_passthrough" {
			key := cassetteKeyForMock(mock, nodePath, opName)
			j.recordings[key] = record
		}
		out, err := j.buildTaskData(record.Outputs, bytesMapToStringMap(record.Artifacts), record.NextTask)
		return out, true, err
	case "replay":
		key := cassetteKeyForMock(mock, nodePath, opName)
		record, ok := j.recordings[key]
		if !ok {
			return nil, true, fmt.Errorf("replay cassette not found for key %q", key)
		}
		out, err := j.buildTaskData(record.Outputs, bytesMapToStringMap(record.Artifacts), record.NextTask)
		return out, true, err
	default:
		return nil, true, fmt.Errorf("mock mode %q not supported in isolated execution", mock.Behavior.Mode)
	}
}

func contextForRunPolicy(runPolicy swf.RunPolicy) (context.Context, context.CancelFunc) {
	if runPolicy.TotalTimeout == nil {
		return context.WithCancel(context.Background())
	}
	timeout := time.Duration(*runPolicy.TotalTimeout)
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}

func (j *testJobContext) selectOpMockForInvocation(invocationKey string, nodePath string, opName string) (OpMock, bool) {
	idx, ok := j.selectOpMockIndexForInvocation(invocationKey, nodePath, opName)
	if !ok {
		return OpMock{}, false
	}
	j.mockSelections[invocationKey] = idx
	j.consumedMockIdxs[idx] = struct{}{}
	return j.caseDef.Mocks.Ops[idx], true
}

func (j *testJobContext) selectOpMockIndexForInvocation(invocationKey string, nodePath string, opName string) (int, bool) {
	if idx, ok := j.mockSelections[invocationKey]; ok {
		if idx >= 0 && idx < len(j.caseDef.Mocks.Ops) {
			return idx, true
		}
		return -1, false
	}

	idx, matched := selectOpMock(j.caseDef.Mocks.Ops, nodePath, opName, j.consumedMockIdxs)
	if !matched {
		return -1, false
	}
	return idx, true
}

func (j *testJobContext) buildTaskData(outputs map[string]interface{}, artifacts map[string]string, nextTask string) (swf.TaskData, error) {
	if outputs == nil {
		outputs = map[string]interface{}{}
	}
	activityOut := workerops.ActivityInvocationOutput{OpOutput: outputs, NextTask: nextTask}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, activityOut)
	if err != nil {
		return nil, err
	}
	j.artifactOrdinal++
	taskOrdinal := j.artifactOrdinal
	artifactList := make([]swf.Artifact, 0, len(artifacts))
	for name, content := range artifacts {
		b := []byte(content)
		j.artifactContents[name] = b
		artifact := swf.NewArtifactFromBytes(name, b)
		swf.AssignArtifactKey(artifact, swf.ArtifactKey{
			JobId:       j.jobKey.JobId,
			TaskOrdinal: taskOrdinal,
			Name:        name,
			SizeBytes:   int64(len(b)),
		})
		artifactList = append(artifactList, artifact)
	}
	return swf.NewTaskData(env, artifactList...)
}

func (j *testJobContext) runPassthroughTask(ctx context.Context, taskType string, inv workerops.ActivityInvocationRequest) (passthroughRecord, error) {
	opName, stepName := splitTaskType(taskType)
	op, exists := coreops.Get(opName)
	if !exists {
		return passthroughRecord{}, fmt.Errorf("passthrough op not registered: %s", opName)
	}
	chain := op.TaskChain()
	if len(chain) == 0 {
		return passthroughRecord{}, fmt.Errorf("passthrough op has no task steps: %s", opName)
	}

	idx := 0
	if stepName != "" {
		found := false
		for i, st := range chain {
			if st.Name == stepName {
				idx = i
				found = true
				break
			}
		}
		if !found {
			return passthroughRecord{}, fmt.Errorf("passthrough task step not found: %s", taskType)
		}
	}

	inputArtifacts := make([]swf.Artifact, 0, len(inv.ArtifactKeys))
	for _, key := range inv.ArtifactKeys {
		content := j.artifactContents[key.Name]
		inputArtifacts = append(inputArtifacts, swf.NewArtifactFromBytes(key.Name, content))
	}

	operationPaths := j.operationPaths
	if strings.TrimSpace(operationPaths.Workdir) == "" {
		operationPaths = recipeTestOperationPaths(filepath.Join(os.TempDir(), "c2j-recipe-tests"))
	}
	if err := ensureRecipeTestOperationDirs(operationPaths); err != nil {
		return passthroughRecord{}, err
	}
	if err := resetRecipeTestDir(operationPaths.Inbox); err != nil {
		return passthroughRecord{}, err
	}
	if err := resetRecipeTestDir(operationPaths.Outbox); err != nil {
		return passthroughRecord{}, err
	}
	if err := j.materializePassthroughArtifacts(operationPaths, inv); err != nil {
		return passthroughRecord{}, err
	}
	pathRuntime := coreops.OperationPathRuntime{
		Views: coreops.OperationPathViews{
			Host: operationPaths,
			Op:   operationPaths,
		},
	}

	deps := coreops.NewOpDependenciesBuilder().
		WithDatabase(j.deps.Database()).
		WithWorkflowControl(j.deps.WorkflowControl()).
		WithArtifacts(inputArtifacts).
		WithJobTool(j).
		WithOperationPaths(operationPaths).
		WithOperationPathRuntime(pathRuntime).
		WithGitContext(coreops.GitExecutionContext{
			BaseRepo:         inv.GitTaskContext.BaseRepo,
			BaseRef:          inv.GitTaskContext.BaseRef,
			ResolvedBaseHash: inv.GitTaskContext.ResolvedBaseHash,
			RecipeSourceRepo: inv.GitTaskContext.RecipeSourceRepo,
			RecipeSourceRef:  inv.GitTaskContext.RecipeSourceRef,
			PersistHash:      inv.GitTaskContext.PersistHash,
			ParentHash:       inv.GitTaskContext.ParentHash,
			CellName:         inv.GitTaskContext.CellName,
			GitAuthor:        inv.GitTaskContext.GitAuthor,
			NodePath:         inv.GitTaskContext.NodePath,
			InvokeSeq:        inv.GitTaskContext.InvokeSeq,
			InvokeHash:       inv.GitTaskContext.InvokeHash,
			WorktreePath:     operationPaths.WorktreePath,
		}).
		WithWorktreePath(operationPaths.WorktreePath).
		Build()

	out, err := chain[idx].Invoke(deps, ctx, inv.Input)
	if err != nil {
		return passthroughRecord{}, fmt.Errorf("passthrough invoke failed for %s: %w", taskType, err)
	}
	nextTask := chain[idx].NextStepTask
	if setter, ok := deps.(interface{ NextTaskType() (string, bool) }); ok {
		if custom, set := setter.NextTaskType(); set {
			nextTask = custom
		}
	}

	artifacts := map[string][]byte{}
	if outDeps, ok := deps.(interface{ GetOutputArtifacts() []swf.Artifact }); ok {
		for _, art := range outDeps.GetOutputArtifacts() {
			if b, err := art.Bytes(ctx); err == nil {
				artifacts[art.Name()] = b
				j.artifactContents[art.Name()] = b
			}
		}
	}
	if err := j.collectPassthroughOutboxArtifacts(operationPaths.Outbox, artifacts); err != nil {
		return passthroughRecord{}, err
	}

	return passthroughRecord{
		Outputs:   out,
		NextTask:  nextTask,
		Artifacts: artifacts,
	}, nil
}

func resetRecipeTestDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("reset recipe test op directory %q: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create recipe test op directory %q: %w", dir, err)
	}
	return nil
}

func (j *testJobContext) materializePassthroughArtifacts(paths coreops.OperationPaths, inv workerops.ActivityInvocationRequest) error {
	for destName, ref := range inv.Artifacts {
		key, ok := ref.StoredKey()
		if !ok {
			continue
		}
		content, ok := j.artifactContents[key.Name]
		if !ok {
			content, ok = j.artifactContents[ref.NameValue()]
		}
		if !ok {
			return fmt.Errorf("passthrough artifact content not found for %q", ref.NameValue())
		}
		dest := filepath.Join(paths.Inbox, filepath.FromSlash(destName))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("materialize passthrough artifact %q: %w", destName, err)
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			return fmt.Errorf("materialize passthrough artifact %q: %w", destName, err)
		}
	}
	return nil
}

func (j *testJobContext) collectPassthroughOutboxArtifacts(outbox string, artifacts map[string][]byte) error {
	if strings.TrimSpace(outbox) == "" {
		return nil
	}
	return filepath.WalkDir(outbox, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(outbox, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		artifacts[name] = content
		j.artifactContents[name] = content
		return nil
	})
}

func selectOpMock(mocks []OpMock, nodePath string, opName string, consumed map[int]struct{}) (int, bool) {
	bestIdx := -1
	bestScore := -1
	for i, m := range mocks {
		if consumed != nil {
			if _, used := consumed[i]; used {
				continue
			}
		}
		nodeMatch := strings.TrimSpace(m.Match.NodePath)
		opMatch := strings.TrimSpace(m.Match.Op)
		score := -1
		switch {
		case nodeMatch != "" && opMatch != "" && nodeMatch == nodePath && opMatch == opName:
			score = 3
		case nodeMatch != "" && opMatch == "" && nodeMatch == nodePath:
			score = 2
		case nodeMatch == "" && opMatch != "" && opMatch == opName:
			score = 1
		}
		if score > bestScore {
			bestIdx = i
			bestScore = score
		}
	}
	if bestIdx < 0 {
		return -1, false
	}
	return bestIdx, true
}

func hasOpMockCandidate(mocks []OpMock, nodePath string, opName string) bool {
	_, matched := selectOpMock(mocks, nodePath, opName, nil)
	return matched
}

func splitTaskType(taskType string) (string, string) {
	if idx := strings.Index(taskType, ":"); idx >= 0 {
		return taskType[:idx], taskType[idx+1:]
	}
	return taskType, ""
}

func cassetteKeyForMock(m OpMock, nodePath string, opName string) string {
	if strings.TrimSpace(m.Behavior.CassetteKey) != "" {
		return strings.TrimSpace(m.Behavior.CassetteKey)
	}
	return nodePath + "::" + opName
}

func bytesMapToStringMap(in map[string][]byte) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = string(v)
	}
	return out
}

func collectArtifactBytes(ctx context.Context, jc *testJobContext, artifacts []swf.Artifact) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range jc.artifactContents {
		out[k] = v
	}
	for _, art := range artifacts {
		if b, err := art.Bytes(ctx); err == nil {
			out[art.Name()] = b
		}
	}
	return out
}

func markFailure(resp *CaseRunResult, category string, reason string) {
	resp.Status = "failed"
	if resp.FailureCategory == "" {
		resp.FailureCategory = category
		resp.FailureReason = reason
	}
}

func parseTimeout(raw string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func runRecipeTestAssertions(assertions []Assertion, outputs map[string]interface{}, artifacts map[string][]byte, executedNodes map[string]bool, status string, vars []RenderedVarsDiagnostic, transitions []TransitionDiagnostic) ([]AssertionResult, bool) {
	results := make([]AssertionResult, 0, len(assertions))
	failed := false
	for _, a := range assertions {
		res := AssertionResult{Type: a.Type, Passed: true}
		switch a.Type {
		case "output_equals":
			actual, _ := lookupPath(outputs, a.Path)
			res.Expected, res.Actual = a.Value, actual
			res.Passed = deepEqualJSON(a.Value, actual)
			if !res.Passed {
				res.Message = "output value mismatch"
			}
		case "output_matches":
			actual, _ := lookupPath(outputs, a.Path)
			actualStr := fmt.Sprintf("%v", actual)
			res.Expected, res.Actual = a.Regex, actualStr
			res.Passed = regexMatch(a.Regex, actualStr)
			if !res.Passed {
				res.Message = "output did not match regex"
			}
		case "artifact_exists":
			_, res.Passed = artifacts[a.Path]
			if !res.Passed {
				res.Message = "artifact missing"
			}
		case "artifact_json_equals":
			b, ok := artifacts[a.Path]
			if !ok {
				res.Passed, res.Message = false, "artifact missing"
				break
			}
			var parsed interface{}
			if err := json.Unmarshal(b, &parsed); err != nil {
				res.Passed, res.Message = false, "artifact is not valid json"
				break
			}
			actual, _ := lookupPath(parsed, a.JsonPath)
			res.Expected, res.Actual = a.Value, actual
			res.Passed = deepEqualJSON(a.Value, actual)
			if !res.Passed {
				res.Message = "artifact json value mismatch"
			}
		case "node_executed":
			res.Passed = executedNodes[a.NodePath]
			if !res.Passed {
				res.Message = "expected node to execute"
			}
		case "node_not_executed":
			res.Passed = !executedNodes[a.NodePath]
			if !res.Passed {
				res.Message = "expected node not to execute"
			}
		case "status_is":
			res.Expected, res.Actual = a.Status, status
			res.Passed = a.Status == status
			if !res.Passed {
				res.Message = "status mismatch"
			}
		case "var_equals":
			actual, found := lookupRenderedVar(vars, a.NodePath, a.Scope, a.Path)
			res.Expected, res.Actual = a.Value, actual
			res.Passed = found && deepEqualJSON(a.Value, actual)
			if !res.Passed {
				res.Message = "rendered var value mismatch"
			}
		case "transition_payload_equals":
			actual, found := lookupTransitionPayload(transitions, a.FromState, a.ToState, a.Path)
			res.Expected, res.Actual = a.Value, actual
			res.Passed = found && deepEqualJSON(a.Value, actual)
			if !res.Passed {
				res.Message = "transition payload value mismatch"
			}
		case "cel_true":
			expr := strings.TrimSpace(a.Expr)
			res.Passed = expr == "" || strings.EqualFold(expr, "true")
			if !res.Passed {
				res.Message = "cel_true currently supports only expression true"
			}
		}
		if !res.Passed {
			failed = true
		}
		results = append(results, res)
	}
	return results, failed
}

func runRecipeTestEvaluations(evals []Evaluation, outputs map[string]interface{}, artifacts map[string][]byte, evalModeOverride string) ([]EvaluationResult, bool) {
	results := make([]EvaluationResult, 0, len(evals))
	enforcedFailed := false
	for _, e := range evals {
		mode := e.Mode
		if mode == "" {
			mode = "enforce"
		}
		if evalModeOverride == "report_only" {
			mode = "report_only"
		}
		res := EvaluationResult{Id: e.ID, Type: e.Type, Mode: mode, Passed: true}
		sourceText, srcErr := resolveEvaluationSource(e.Source, outputs, artifacts)
		if srcErr != nil {
			res.Passed = false
			res.ErrorCategory = "evaluator_error"
			res.ErrorMessage = srcErr.Error()
			if mode == "enforce" {
				enforcedFailed = true
			}
			results = append(results, res)
			continue
		}
		switch e.Type {
		case "text_pattern":
			res.Passed, res.Output = evaluateTextPattern(sourceText, e.Config)
		case "llm_judge":
			res.Passed, res.Output = evaluateLLMJudge(sourceText, e.Config)
		default:
			res.Passed = false
			res.ErrorCategory = "evaluator_error"
			res.ErrorMessage = "unsupported evaluation type"
		}
		if !res.Passed && mode == "enforce" {
			enforcedFailed = true
		}
		results = append(results, res)
	}
	return results, enforcedFailed
}

func resolveEvaluationSource(source EvalSource, outputs map[string]interface{}, artifacts map[string][]byte) (string, error) {
	switch source.Kind {
	case "artifact":
		b, ok := artifacts[source.Path]
		if !ok {
			return "", fmt.Errorf("artifact source not found: %s", source.Path)
		}
		return string(b), nil
	case "artifact_glob":
		names := make([]string, 0, len(artifacts))
		for n := range artifacts {
			names = append(names, n)
		}
		sort.Strings(names)
		matches := make([]string, 0)
		for _, n := range names {
			if ok, _ := path.Match(source.Glob, n); ok {
				matches = append(matches, string(artifacts[n]))
			}
		}
		if len(matches) == 0 {
			return "", fmt.Errorf("artifact glob matched nothing: %s", source.Glob)
		}
		return strings.Join(matches, "\n"), nil
	case "output_path":
		v, ok := lookupPath(outputs, source.OutputPath)
		if !ok {
			return "", fmt.Errorf("output path not found: %s", source.OutputPath)
		}
		return fmt.Sprintf("%v", v), nil
	default:
		return "", fmt.Errorf("source kind %s is not available in this runtime", source.Kind)
	}
}

func evaluateTextPattern(source string, cfg map[string]interface{}) (bool, map[string]interface{}) {
	passed := true
	out := map[string]interface{}{"require_matches": []string{}, "forbid_matches": []string{}}
	if cfg == nil {
		return true, out
	}
	if require, ok := stringSliceFromAny(cfg["require_regex"]); ok {
		for _, r := range require {
			if regexMatch(r, source) {
				out["require_matches"] = append(out["require_matches"].([]string), r)
			} else {
				passed = false
			}
		}
	}
	if forbid, ok := stringSliceFromAny(cfg["forbid_regex"]); ok {
		for _, r := range forbid {
			if regexMatch(r, source) {
				out["forbid_matches"] = append(out["forbid_matches"].([]string), r)
				passed = false
			}
		}
	}
	out["length"] = len(source)
	return passed, out
}

func evaluateLLMJudge(source string, cfg map[string]interface{}) (bool, map[string]interface{}) {
	verdict, score := "pass", 1.0
	findings := []string{}
	if strings.Contains(strings.ToLower(source), "fail") {
		verdict, score = "fail", 0
		findings = append(findings, "content contained token 'fail'")
	}
	out := map[string]interface{}{"verdict": verdict, "score": score, "findings": findings}
	passWhen := ""
	if cfg != nil {
		if v, ok := cfg["pass_when"].(string); ok {
			passWhen = strings.TrimSpace(v)
		}
	}
	if passWhen == "" {
		return verdict == "pass", out
	}
	return evalPassWhen(passWhen, verdict, score), out
}

func evalPassWhen(expr string, verdict string, score float64) bool {
	e := strings.ReplaceAll(expr, " ", "")
	if strings.Contains(e, "&&") {
		for _, p := range strings.Split(e, "&&") {
			if !evalPassWhen(p, verdict, score) {
				return false
			}
		}
		return true
	}
	switch {
	case e == "verdict=='pass'" || e == `verdict=="pass"`:
		return verdict == "pass"
	case strings.HasPrefix(e, "score>="):
		v, _ := strconv.ParseFloat(strings.TrimPrefix(e, "score>="), 64)
		return score >= v
	case strings.HasPrefix(e, "score>"):
		v, _ := strconv.ParseFloat(strings.TrimPrefix(e, "score>"), 64)
		return score > v
	case strings.HasPrefix(e, "score<="):
		v, _ := strconv.ParseFloat(strings.TrimPrefix(e, "score<="), 64)
		return score <= v
	case strings.HasPrefix(e, "score<"):
		v, _ := strconv.ParseFloat(strings.TrimPrefix(e, "score<"), 64)
		return score < v
	case strings.HasPrefix(e, "score=="):
		v, _ := strconv.ParseFloat(strings.TrimPrefix(e, "score=="), 64)
		return score == v
	default:
		return false
	}
}

func inlineArtifacts(artifacts map[string][]byte, maxBytes int64) map[string]InlineArtifact {
	out := make(map[string]InlineArtifact, len(artifacts))
	names := make([]string, 0, len(artifacts))
	for n := range artifacts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := artifacts[n]
		r := InlineArtifact{SizeBytes: int64(len(b))}
		if int64(len(b)) > maxBytes {
			r.Truncated = true
			b = b[:maxBytes]
		}
		r.ContentBase64 = base64.StdEncoding.EncodeToString(b)
		out[n] = r
	}
	return out
}

func lookupPath(root interface{}, p string) (interface{}, bool) {
	if strings.TrimSpace(p) == "" {
		return root, true
	}
	cur := root
	for _, part := range strings.Split(p, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		next, ok := m[part]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func lookupRenderedVar(vars []RenderedVarsDiagnostic, nodePath string, scope string, varPath string) (interface{}, bool) {
	nodePath = strings.TrimSpace(nodePath)
	scope = strings.TrimSpace(scope)
	for i := len(vars) - 1; i >= 0; i-- {
		item := vars[i]
		if nodePath != "" && strings.TrimSpace(item.NodePath) != nodePath {
			continue
		}
		if scope != "" && strings.TrimSpace(item.Scope) != scope {
			continue
		}
		return lookupPath(item.Vars, varPath)
	}
	return nil, false
}

func lookupTransitionPayload(transitions []TransitionDiagnostic, fromState string, toState string, payloadPath string) (interface{}, bool) {
	fromState = strings.TrimSpace(fromState)
	toState = strings.TrimSpace(toState)
	for i := len(transitions) - 1; i >= 0; i-- {
		item := transitions[i]
		if !item.Selected {
			continue
		}
		if fromState != "" && strings.TrimSpace(item.FromState) != fromState {
			continue
		}
		if toState != "" && strings.TrimSpace(item.ToState) != toState {
			continue
		}
		return lookupPath(item.Payload, payloadPath)
	}
	return nil, false
}

func redactVarsDiagnostics(in []RenderedVarsDiagnostic) []RenderedVarsDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]RenderedVarsDiagnostic, 0, len(in))
	for _, item := range in {
		redacted, _ := redact.Value("vars", item.Vars).(map[string]interface{})
		out = append(out, RenderedVarsDiagnostic{
			NodePath: item.NodePath,
			Scope:    item.Scope,
			Vars:     redacted,
		})
	}
	return out
}

func redactTransitionDiagnostics(in []TransitionDiagnostic) []TransitionDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]TransitionDiagnostic, 0, len(in))
	for _, item := range in {
		redacted, _ := redact.Value("transition.payload", item.Payload).(map[string]interface{})
		item.Payload = redacted
		out = append(out, item)
	}
	return out
}

func cloneStringMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneValue(value)
	}
	return out
}

func cloneValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneStringMap(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return typed
	}
}

func regexMatch(pattern string, value string) bool {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func deepEqualJSON(a interface{}, b interface{}) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
	return string(ab) == string(bb)
}

func stringSliceFromAny(v interface{}) ([]string, bool) {
	switch t := v.(type) {
	case []string:
		return t, true
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, it := range t {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

func failureCategoryFromError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	e := strings.ToLower(err.Error())
	switch {
	case strings.Contains(e, "policy") || strings.Contains(e, "blocked") || strings.Contains(e, "target scope") || strings.Contains(e, "requires op mock"):
		return "policy_blocked"
	case strings.Contains(e, "timeout"):
		return "timeout"
	default:
		return "runtime_error"
	}
}
