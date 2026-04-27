package ops

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/logutil"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"
)

type opExecutor struct {
	deps       ops.ServiceDependencies2
	reg        ActivityRegistration
	controller *gitstate.Controller
}

type nextTaskOverride interface {
	NextTaskType() (string, bool)
}

func unchangedGitResult(input gitstate.GlobalGitTaskContext) contextual.GitCommitContext {
	if input.PersistHash != "" {
		return contextual.GitCommitContext{
			PersistHash: input.PersistHash,
			ParentHash:  input.ParentHash,
		}
	}
	return contextual.GitCommitContext{
		ParentRef: input.BaseRef,
	}
}

func currentGitResult(task *gitstate.GitTaskContext) contextual.GitCommitContext {
	parentRef := ""
	if task.PersistHash == "" {
		parentRef = task.BaseRef
	}
	return contextual.GitCommitContext{
		PersistHash: task.PersistHash,
		ParentHash:  task.ParentHash,
		ParentRef:   parentRef,
	}
}

func (t opExecutor) do(ctx context.Context, jobTool ops.JobTool, req ActivityInvocationRequest, inputArtifacts []swf.Artifact) (output ActivityInvocationOutput, outputArtifacts []swf.Artifact, err error) {
	deps := t.deps
	controller := t.controller
	reg := t.reg

	var zero ActivityInvocationOutput

	logger := slog.Default().With(
		"task_type", reg.TaskType,
		"op_type", reg.Metadata.Type,
		"step", reg.Step.Name,
		"step_index", reg.StepIndex,
		"cell_name", req.GitTaskContext.CellName,
		"cell_path", req.GitTaskContext.CellPath,
		"node_path", req.GitTaskContext.NodePath,
		"invoke_seq", req.GitTaskContext.InvokeSeq,
		"invoke_hash", req.GitTaskContext.InvokeHash,
		"input_artifact_count", len(inputArtifacts),
		"artifact_key_count", len(req.ArtifactKeys),
		"artifact_binding_count", len(req.Artifacts),
	)
	if jobTool != nil {
		key := jobTool.GetJobKey()
		logger = logger.With("tenant_id", key.TenantId, "job_id", key.JobId)
	}
	start := time.Now()

	// Create temporary worktree directory for this invocation
	workDir, err := createWorkDir()
	if err != nil {
		return zero, nil, fmt.Errorf("create temp worktree: %w", err)
	}
	worktreePath := filepath.Join(workDir, "worktree")

	defer func() {
		if err == nil {
			return
		}
		logger.Error("opExecutor.do failed",
			"duration_ms", time.Since(start).Milliseconds(),
			"work_dir", workDir,
			"worktree_path", worktreePath,
			"error", err,
			"error_chain", logutil.ErrorChain(err),
			"stacktrace", logutil.Stacktrace(6),
		)
	}()

	inbox := filepath.Join(workDir, "inbox")
	err = os.Mkdir(inbox, 0o755)
	if err != nil {
		return zero, nil, err
	}
	outbox := filepath.Join(workDir, "outbox")
	err = os.Mkdir(outbox, 0o755)
	if err != nil {
		return zero, nil, err
	}

	if jobTool == nil {
		return zero, nil, fmt.Errorf("job tool is required")
	}
	replacements := map[string]string{
		contextual.WorktreePathSentinel:   worktreePath,
		contextual.WorkdirPathSentinel:    workDir,
		contextual.ArtifactInboxSentinel:  inbox,
		contextual.ArtifactOutboxSentinel: outbox,
		contextual.JobIdSentinel:          jobTool.GetJobKey().JobId,
	}

	defer removeWorkDir(worktreePath)

	incomingGitContext := req.GitTaskContext

	// Build full GitTaskContext for controller from global context + local worktree path
	fullContext := &gitstate.GitTaskContext{
		GlobalGitTaskContext: &req.GitTaskContext,
		WorktreePath:         worktreePath,
	}
	if fullContext.GlobalGitTaskContext == nil {
		return zero, nil, fmt.Errorf("git task context is required")
	}

	// Rehydrate referenced artifacts from keys.
	if len(req.ArtifactKeys) > 0 {

		ctl := deps.WorkflowControl()
		if ctl == nil {
			return zero, nil, fmt.Errorf("workflow control is required for artifact resolution")
		}
		rehydrated := make([]swf.Artifact, 0, len(req.ArtifactKeys))
		for _, key := range req.ArtifactKeys {
			artifact := ctl.GetArtifactLazy(ctx, jobTool.GetJobKey().TenantId, key)
			rehydrated = append(rehydrated, artifact)
		}
		inputArtifacts = append(inputArtifacts, rehydrated...)
	}

	artifactByKey := indexArtifactsByKey(inputArtifacts)
	if len(req.Artifacts) > 0 {
		if err := materializeArtifactBindings(ctx, inbox, req.Artifacts, artifactByKey); err != nil {
			return zero, nil, err
		}
	}

	// Find and filter input thin pack artifact
	var thinPackArtifact swf.Artifact
	var nonThinPackArtifacts []swf.Artifact

	for _, art := range inputArtifacts {
		if art.Name() == gitstate.ThinPackArtifactName {
			thinPackArtifact = art
		} else {
			nonThinPackArtifacts = append(nonThinPackArtifacts, art)
		}
	}

	// Call Restore with full context (includes WorktreePath)
	if err := controller.Restore(context.Background(), fullContext, thinPackArtifact); err != nil {
		return zero, nil, err
	}

	// CRITICAL: Hydrate sentinel values in input with actual worktree path
	// Templates like {{ environment.worktree_path }} resolved to sentinel at compile time
	// Now replace with real local path
	hydratedInput := replaceSentinels(req.Input, replacements)

	// Build OpDependencies with WorktreePath and filtered artifacts (thin pack hidden from operation)
	db := deps.Database()
	if tx, ok := swf.TxFromCtx(ctx); ok && tx != nil {
		db = tx
	}
	opDeps := ops.NewOpDependenciesBuilder().
		WithArtifacts(nonThinPackArtifacts).
		WithJobTool(jobTool).
		WithDatabase(db).
		WithWorkflowControl(deps.WorkflowControl()).
		WithGitContext(ops.GitExecutionContext{
			BaseRepo:         fullContext.GetBaseRepo(),
			BaseRef:          fullContext.GetBaseRef(),
			ResolvedBaseHash: fullContext.GetResolvedBaseHash(),
			RecipeSourceRepo: fullContext.GetRecipeSourceRepo(),
			RecipeSourceRef:  fullContext.GetRecipeSourceRef(),
			PersistHash:      fullContext.GetPersistHash(),
			ParentHash:       fullContext.GetParentHash(),
			CellName:         fullContext.GetCellName(),
			CellPath:         fullContext.GetCellPath(),
			GitAuthor:        fullContext.GetGitAuthor(),
			NodePath:         fullContext.GetNodePath(),
			InvokeSeq:        fullContext.GetInvokeSeq(),
			InvokeHash:       req.GitTaskContext.InvokeHash,
			WorktreePath:     fullContext.WorktreePath,
		}).
		WithWorktreePath(fullContext.WorktreePath).
		Build()

	// Ensure artifacts are collected on both success and failure paths
	defer func() {
		artifacts := opDeps.GetOutputArtifacts()
		outputArtifacts = append(outputArtifacts, artifacts...)
	}()

	// Execute operation with HYDRATED input (sentinels replaced)
	outputData, stepErr := reg.Step.Invoke(opDeps, ctx, hydratedInput)
	nextTask := reg.NextTaskType
	if override, ok := opDeps.(nextTaskOverride); ok {
		if overrideValue, set := override.NextTaskType(); set {
			nextTask = overrideValue
		}
	}
	if err := filepath.WalkDir(outbox, func(path string, d fs.DirEntry, walkErr error) error {
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
		artifact, err := swf.NewArtifactFromFile(rel, path)
		if err != nil {
			return err
		}
		outputArtifacts = append(outputArtifacts, artifact)
		return nil
	}); err != nil {
		if stepErr != nil {
			return ActivityInvocationOutput{
				OpOutput:     outputData,
				NextTask:     nextTask,
				ArtifactRefs: opDeps.GetExternalArtifacts(),
			}, outputArtifacts, errors.Join(stepErr, fmt.Errorf("collect outbox artifacts: %w", err))
		}
		return zero, outputArtifacts, err
	}

	if req.Const {
		if thinPackArtifact != nil {
			outputArtifacts = append(outputArtifacts, thinPackArtifact)
		}
		output = ActivityInvocationOutput{
			OpOutput:     outputData,
			GitResult:    unchangedGitResult(incomingGitContext),
			NextTask:     nextTask,
			ArtifactRefs: opDeps.GetExternalArtifacts(),
		}
		if stepErr != nil {
			return output, outputArtifacts, stepErr
		}
		return output, outputArtifacts, nil
	}

	// Call PersistWithDiffs with full context
	persistOutput, persistArtifacts, err := controller.PersistWithDiffs(context.Background(), fullContext)
	if err != nil {
		if stepErr != nil {
			return ActivityInvocationOutput{
				OpOutput:     outputData,
				NextTask:     nextTask,
				ArtifactRefs: opDeps.GetExternalArtifacts(),
			}, outputArtifacts, errors.Join(stepErr, fmt.Errorf("persist workspace changes: %w", err))
		}
		return zero, outputArtifacts, err
	}

	gitResult := currentGitResult(fullContext)

	// Handle artifact pass-through logic
	if persistOutput != nil && persistOutput.HasChanges {
		// PersistWithDiffs created artifacts (changes were made)
		// Append all artifacts (thin pack + diffs) to output
		outputArtifacts = append(outputArtifacts, persistArtifacts...)
	} else {
		// Preserve input git state for unchanged tasks so hash-mode never regresses to ref-mode.
		gitResult = unchangedGitResult(incomingGitContext)
		if thinPackArtifact != nil {
			// No changes, but we had an input thin pack - pass through the SAME artifact
			// This avoids re-uploading; SWF can reuse the existing artifact
			outputArtifacts = append(outputArtifacts, thinPackArtifact)
		}
	}

	output = ActivityInvocationOutput{
		OpOutput:     outputData,
		GitResult:    gitResult,
		NextTask:     nextTask,
		ArtifactRefs: opDeps.GetExternalArtifacts(),
	}
	if stepErr != nil {
		return output, outputArtifacts, stepErr
	}
	return output, outputArtifacts, nil
}

// replaceSentinels recursively walks the input map and replaces sentinel values with actual worktree path
func replaceSentinels(input map[string]interface{}, replacements map[string]string) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range input {
		result[k] = replaceSentinelValue(v, replacements)
	}
	return result
}

func replaceValue(val string, replacements map[string]string) string {
	if replacement, exists := replacements[val]; exists {
		return replacement
	}
	for sentinel, replacement := range replacements {
		if strings.Contains(val, sentinel) {
			val = strings.ReplaceAll(val, sentinel, replacement)
		}
	}
	return val
}

// replaceSentinelValue handles different types recursively
func replaceSentinelValue(value interface{}, replacements map[string]string) interface{} {
	switch v := value.(type) {
	case string:
		return replaceValue(v, replacements)
	case map[string]interface{}:
		return replaceSentinels(v, replacements)
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = replaceSentinelValue(item, replacements)
		}
		return result
	default:
		return v
	}
}

func indexArtifactsByKey(artifacts []swf.Artifact) map[string]swf.Artifact {
	index := make(map[string]swf.Artifact, len(artifacts))
	for _, artifact := range artifacts {
		key, err := artifact.ArtifactKey()
		if err != nil {
			continue
		}
		index[artifactKeyIdentity(key)] = artifact
	}
	return index
}

func materializeArtifactBindings(ctx context.Context, inbox string, bindings map[string]recipeartifacts.Ref, artifactsByKey map[string]swf.Artifact) error {
	for name, artifactRef := range bindings {
		if err := validateBindingName(name); err != nil {
			return fmt.Errorf("invalid artifact binding %q: %w", name, err)
		}

		if key, ok := artifactRef.StoredKey(); ok {
			artifact, found := artifactsByKey[artifactKeyIdentity(key)]
			if !found {
				return fmt.Errorf("artifact binding %q refers to missing artifact %s", name, artifactKeyIdentity(key))
			}
			if err := materializeStoredArtifactBinding(ctx, inbox, name, artifact); err != nil {
				return fmt.Errorf("artifact binding %q save failed: %w", name, err)
			}
			continue
		}

		if artifactRef.External != nil {
			if err := materializeExternalArtifactBinding(ctx, inbox, name, artifactRef); err != nil {
				return fmt.Errorf("artifact binding %q materialization failed: %w", name, err)
			}
			continue
		}

		return fmt.Errorf("artifact binding %q has unsupported ref kind %q", name, artifactRef.Kind)
	}
	return nil
}

func materializeStoredArtifactBinding(ctx context.Context, inbox string, name string, artifact swf.Artifact) error {
	destPath, err := bindingDestination(inbox, name, artifact.Name())
	if err != nil {
		return fmt.Errorf("invalid destination: %w", err)
	}
	if err := ensureDestinationAbsent(destPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("mkdir failed: %w", err)
	}
	if err := artifact.SaveToFile(ctx, destPath); err != nil {
		return err
	}
	return nil
}

func materializeExternalArtifactBinding(ctx context.Context, inbox string, name string, artifactRef recipeartifacts.Ref) error {
	if artifactRef.External == nil {
		return fmt.Errorf("missing external payload")
	}
	parsed, err := url.Parse(artifactRef.External.URL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}

	switch parsed.Scheme {
	case "file":
		return materializeFileURLBinding(inbox, name, artifactRef, parsed)
	case "http", "https":
		return materializeHTTPBinding(ctx, inbox, name, artifactRef, parsed.String())
	default:
		return fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}
}

func materializeFileURLBinding(inbox string, name string, artifactRef recipeartifacts.Ref, parsed *url.URL) error {
	sourcePath := parsed.Path
	if sourcePath == "" {
		return fmt.Errorf("file url path cannot be empty")
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}

	if info.IsDir() {
		destRoot, err := expandedBindingDestination(inbox, name)
		if err != nil {
			return err
		}
		if err := ensureDestinationAbsent(destRoot); err != nil {
			return err
		}
		return copyDirectory(sourcePath, destRoot)
	}

	if artifactRef.External.Expand {
		destRoot, err := expandedBindingDestination(inbox, name)
		if err != nil {
			return err
		}
		if err := ensureDestinationAbsent(destRoot); err != nil {
			return err
		}
		return extractArchive(sourcePath, destRoot)
	}

	destPath, err := bindingDestination(inbox, name, artifactRef.NameValue())
	if err != nil {
		return err
	}
	if err := ensureDestinationAbsent(destPath); err != nil {
		return err
	}
	return copyFile(sourcePath, destPath)
}

func materializeHTTPBinding(ctx context.Context, inbox string, name string, artifactRef recipeartifacts.Ref, sourceURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	if artifactRef.External != nil && artifactRef.External.Expand {
		tmpDir, err := os.MkdirTemp("", "external-artifact-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmpDir)

		tmpPath := filepath.Join(tmpDir, archiveTempName(artifactRef, sourceURL))
		if err := copyReaderToFile(resp.Body, tmpPath); err != nil {
			return err
		}

		destRoot, err := expandedBindingDestination(inbox, name)
		if err != nil {
			return err
		}
		if err := ensureDestinationAbsent(destRoot); err != nil {
			return err
		}
		return extractArchive(tmpPath, destRoot)
	}

	destPath, err := bindingDestination(inbox, name, artifactRef.NameValue())
	if err != nil {
		return err
	}
	if err := ensureDestinationAbsent(destPath); err != nil {
		return err
	}
	return copyReaderToFile(resp.Body, destPath)
}

func archiveTempName(artifactRef recipeartifacts.Ref, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil {
		if base := path.Base(parsed.Path); base != "" && base != "." && base != "/" {
			return base
		}
	}
	name := strings.TrimSpace(artifactRef.NameValue())
	if name == "" {
		return "artifact.bin"
	}
	return name
}

func copyReaderToFile(r io.Reader, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return nil
}

func copyFile(sourcePath string, destPath string) error {
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()
	return copyReaderToFile(in, destPath)
}

func copyDirectory(sourceRoot string, destRoot string) error {
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(sourceRoot, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, current)
		if err != nil {
			return err
		}
		target := filepath.Join(destRoot, rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(current, target)
	})
}

func extractArchive(sourcePath string, destRoot string) error {
	lower := strings.ToLower(sourcePath)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(sourcePath, destRoot)
	case strings.HasSuffix(lower, ".tar"):
		return extractTarArchive(sourcePath, destRoot)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGzArchive(sourcePath, destRoot)
	default:
		return fmt.Errorf("unsupported archive format: %s", filepath.Base(sourcePath))
	}
}

func extractZip(sourcePath string, destRoot string) error {
	r, err := zip.OpenReader(sourcePath)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	for _, file := range r.File {
		target, err := archiveTargetPath(destRoot, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		err = copyReaderToFile(rc, target)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarArchive(sourcePath string, destRoot string) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractTarStream(f, destRoot)
}

func extractTarGzArchive(sourcePath string, destRoot string) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()
	return extractTarStream(gzr, destRoot)
}

func extractTarStream(r io.Reader, destRoot string) error {
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := archiveTargetPath(destRoot, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := copyReaderToFile(tr, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry type %d", hdr.Typeflag)
		}
	}
}

func archiveTargetPath(destRoot string, entryName string) (string, error) {
	cleanName := filepath.Clean(entryName)
	if cleanName == "." || cleanName == "" {
		return destRoot, nil
	}
	target := filepath.Clean(filepath.Join(destRoot, cleanName))
	if !strings.HasPrefix(target, destRoot+string(filepath.Separator)) && target != destRoot {
		return "", fmt.Errorf("archive entry escapes destination")
	}
	return target, nil
}

func ensureDestinationAbsent(destPath string) error {
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("would overwrite %s", destPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat failed: %w", err)
	}
	return nil
}

func validateBindingName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("name must be relative")
	}
	for _, segment := range splitPathSegments(name) {
		if segment == ".." {
			return fmt.Errorf("name must not contain '..' segments")
		}
	}
	return nil
}

func bindingDestination(inbox, name, artifactName string) (string, error) {
	if hasTrailingSlash(name) {
		trimmed := strings.TrimRight(name, "/\\")
		if trimmed == "" {
			return "", fmt.Errorf("name cannot be root")
		}
		name = filepath.Join(trimmed, artifactName)
	}
	destPath := filepath.Clean(filepath.Join(inbox, name))
	if !strings.HasPrefix(destPath, inbox+string(filepath.Separator)) && destPath != inbox {
		return "", fmt.Errorf("destination escapes inbox")
	}
	return destPath, nil
}

func expandedBindingDestination(inbox string, name string) (string, error) {
	trimmed := strings.TrimRight(name, "/\\")
	if trimmed == "" {
		return "", fmt.Errorf("name cannot be root")
	}
	destPath := filepath.Clean(filepath.Join(inbox, trimmed))
	if !strings.HasPrefix(destPath, inbox+string(filepath.Separator)) && destPath != inbox {
		return "", fmt.Errorf("destination escapes inbox")
	}
	return destPath, nil
}

func splitPathSegments(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

func hasTrailingSlash(name string) bool {
	return strings.HasSuffix(name, "/") || strings.HasSuffix(name, "\\")
}

func artifactKeyIdentity(key swf.ArtifactKey) string {
	return fmt.Sprintf("%s:%d:%s", key.JobId, key.TaskOrdinal, key.Name)
}
