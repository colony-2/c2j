package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const hookPolicyTaskType = "recipe_hook_policy"
const hookResolveTaskType = "recipe_hook:resolve"

// Implicit workspaces can start with a floating ref. Record the initial base
// before recipe operations can advance it, even if the first hook follows a rebase.
func initialHookBase(ctx workflow.Context, parent *template.ResolutionContext) (string, error) {
	g := parent.TaskExecutionContext().GitTask
	if ws := parent.TaskExecutionContext().Workspace; ws != nil && ws.ScopeID != "" {
		return ws.InitialHash, nil
	}
	if g.ParentHash != "" {
		return g.ParentHash, nil
	}
	if g.ResolvedBaseHash != "" {
		return g.ResolvedBaseHash, nil
	}
	if parent.Options.Mode == template.ModeValidate {
		return strings.Repeat("0", 40), nil
	}
	p := parent.Permissions
	out, err := workflow.ValueCall(ctx.JobContext, "hook-initial-base", WorkspaceResolutionTaskType, jobdb.RunPolicy{}, WorkspaceResolutionInput{
		Cell: g.BaseRepo, Ref: g.BaseRef, ScopeID: "hook-initial-base",
		Resolution: cellref.Context{SelfRepo: g.BaseRepo, SelfRef: g.BaseRef},
		GitProfile: parent.GitProfile, Permissions: &p,
	})
	if err != nil {
		return "", err
	}
	raw, err := out.GetData()
	if err != nil {
		return "", err
	}
	var result WorkspaceResolutionOutput
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if !isFullGitHash(result.Workspace.InitialHash) {
		return "", fmt.Errorf("invalid initial hook base")
	}
	return result.Workspace.InitialHash, nil
}

type hookPolicyWorker struct{}

func (hookPolicyWorker) Name() string { return hookPolicyTaskType }
func (w hookPolicyWorker) Run(tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, data)
}
func (hookPolicyWorker) RunTask(ctx context.Context, tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	var req struct {
		Repository string `json:"repository"`
	}
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	id, err := hookpolicy.Identity(req.Repository)
	if err != nil {
		return nil, err
	}
	s, err := tc.GetConfiguration(ctx)
	if err != nil {
		return nil, err
	}
	policy, err := hookpolicy.Capture(s, id)
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(policy)
}

func hasCellHooks(r recipe.Recipe) bool {
	var walk func(recipe.Node) bool
	walk = func(n recipe.Node) bool {
		switch v := n.NodeImpl.(type) {
		case *recipe.NodeInclude:
			return strings.HasPrefix(v.Include.Recipe, "cell://")
		case *recipe.NodeSequence:
			for _, n := range v.Sequence {
				if walk(n) {
					return true
				}
			}
		case *recipe.NodeState:
			if v.States != nil {
				for _, s := range v.States.States {
					if walk(s.Node) {
						return true
					}
				}
			}
		}
		return false
	}
	switch v := r.RecipeImpl.(type) {
	case *recipe.RecipeSequence:
		for _, n := range v.Sequence {
			if walk(n) {
				return true
			}
		}
	case *recipe.RecipeState:
		if v.States != nil {
			for _, s := range v.States.States {
				if walk(s.Node) {
					return true
				}
			}
		}
	}
	return false
}

type hookResolution struct {
	Path               string                       `json:"path"`
	Candidate          string                       `json:"candidate"`
	Absent             bool                         `json:"absent"`
	RecipeYAMLArtifact string                       `json:"recipe_yaml_artifact,omitempty"`
	RecipeYAML         string                       `json:"recipe_yaml,omitempty"`
	ContentSHA256      string                       `json:"content_sha256,omitempty"`
	Source             recipe.RecipeSourceSnapshot  `json:"source"`
	Functions          WithinRecipeResolutionResult `json:"functions,omitempty"`
}
type hookResolveWorker struct{}

func (hookResolveWorker) Name() string { return hookResolveTaskType }
func (w hookResolveWorker) Run(tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, data)
}
func (w hookResolveWorker) RunTask(ctx context.Context, tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	if permissions.FromContext(ctx) == nil {
		return nil, fmt.Errorf("hook resolution requires a guarded task worker")
	}
	var req workerops.ActivityInvocationRequest
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	id, err := hookpolicy.Identity(req.GitTaskContext.BaseRepo)
	if err != nil || id != req.Input["repository"] {
		return nil, fmt.Errorf("cell hook requires the consuming repository workspace")
	}
	hook, ok := req.Input["hook"].(string)
	if !ok {
		return nil, fmt.Errorf("missing hook path")
	}
	if _, err = hookpolicy.Path(hook); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "c2j-hook-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	g := gitstate.GitTaskContext{GlobalGitTaskContext: &req.GitTaskContext, WorktreePath: filepath.Join(dir, "repo")}
	arts, err := data.GetArtifacts()
	if err != nil {
		return nil, err
	}
	var pack jobdb.Artifact
	for _, a := range arts {
		if req.WorkspaceManaged {
			key, e := a.ArtifactKey()
			if e == nil && req.RestoreArtifact != nil && reflect.DeepEqual(key, *req.RestoreArtifact) {
				if pack != nil {
					return nil, fmt.Errorf("multiple restore artifacts")
				}
				pack = a
			}
		} else if a.Name() == gitstate.ThinPackArtifactName {
			pack = a
		}
	}
	if req.RestoreArtifact != nil && pack == nil {
		return nil, fmt.Errorf("hook workspace restore artifact missing")
	}
	if err = gitstate.NewController(nil).Restore(ctx, &g, pack); err != nil {
		return nil, err
	}
	head, err := common.GetCommitHash(ctx, g.WorktreePath, "HEAD")
	if err != nil {
		return nil, err
	}
	result := hookResolution{Path: hook, Candidate: head}
	source, _ := req.Input["source_commit"].(string)
	phase, _ := req.Input["phase"].(string)
	if source == "" && phase == "pin" {
		mode, _ := req.Input["source_hooks"].(string)
		if err := recipe.HookSource(mode).Validate(); err != nil {
			return nil, err
		}
		switch recipe.HookSource(mode) {
		case "", recipe.HooksFromCurrent:
			source = head
		case recipe.HooksFromBase:
			source, _ = req.Input["initial_base"].(string)
			if g.Workspace != nil {
				source = g.Workspace.InitialHash
			}
		default:
			source = g.ResolvedBaseHash
		}
	}
	if !isFullGitHash(source) {
		return nil, fmt.Errorf("hook source requires a recorded base commit; use from_current for an empty-base workspace")
	}
	sourceRepo := req.GitTaskContext.BaseRepo
	if !strings.Contains(sourceRepo, "://") {
		sourceRepo = "file://" + sourceRepo
	}
	selector := fmt.Sprintf("git+%s//%s@%s", sourceRepo, hook, source)
	resolution := RecipeSourceResolution{SourceKind: RecipeSourceKindGit, SubmittedSelector: "cell://" + hook, ResolvedSelector: selector, ResolvedCommit: source, WasAlreadyPinned: true}
	result.Source = recipeSourceSnapshot(resolution)
	loader := &candidateRecipeResolver{RecipeSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{}), dir: g.WorktreePath, repository: sourceRepo, commit: source}
	var recipeArtifact jobdb.Artifact
	if phase == "load" {
		if req.GitTaskContext.PersistHash == "" || head != req.GitTaskContext.PersistHash {
			return nil, fmt.Errorf("hook load requires the recorded candidate")
		}
		// Only this disposable source checkout changes. The returned Git state and
		// thin pack still describe the candidate that will execute the hook.
		if source != head {
			if !common.CommitExists(ctx, g.WorktreePath, source) {
				if _, err := common.ExecuteGitCommand(ctx, g.WorktreePath, "fetch", "origin", source); err != nil {
					return nil, err
				}
			}
			if _, err := common.ExecuteGitCommand(ctx, g.WorktreePath, "checkout", "--detach", source); err != nil {
				return nil, err
			}
		}
		contents, err := loader.read(hook)
		if errors.Is(err, os.ErrNotExist) {
			result.Absent = true
		} else if err != nil {
			return nil, err
		} else {
			var entry hookpolicy.Entry
			encoded, err := json.Marshal(req.Input["policy"])
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(encoded, &entry); err != nil {
				return nil, err
			}
			parent := permissions.State{Version: permissions.Version}
			if req.Permissions != nil {
				parent = *req.Permissions
			}
			var call *permissions.Spec
			encoded, err = json.Marshal(req.Input["perms"])
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(encoded, &call); err != nil {
				return nil, err
			}
			authority, err := entry.Authority(head, parent, call)
			if err != nil {
				return nil, err
			}
			ctx = gitprofile.WithSecretLimit(ctx, authority.Limit.Secrets.Resolve)
			rec, err := recipe.LoadRecipeFromString(contents)
			if err != nil {
				return nil, err
			}
			// Relative includes follow the pinned hook source, not the candidate.
			expanded, err := ResolveInlineRecipes(ctx, *rec, InlineResolutionOptions{RootSource: &resolution, Resolver: loader})
			if err != nil {
				return nil, err
			}
			if err := bindHookIncludeSources(expanded.Recipe); err != nil {
				return nil, err
			}
			// Use the ordinary CEL import resolver and record floating-ref resolutions
			// alongside this invocation's recipe definition for retries and replay.
			var selectors []string
			for _, imp := range expanded.Recipe.GetMetadata().Extensions.Functions {
				if selectorNeedsResolution(imp.Selector) {
					selectors = append(selectors, imp.Selector)
				}
			}
			result.Functions, err = resolveSelectors(ctx, selectors, extops.ResolveOptions{RepositorySource: sourceRepo, RepositoryRef: source})
			if err != nil {
				return nil, err
			}
			yaml, err := marshalRecipeYAML(expanded.Recipe)
			if err != nil {
				return nil, err
			}
			result.RecipeYAMLArtifact = ResolvedRecipeYAMLArtifactName
			recipeArtifact = jobdb.NewArtifactFromBytes(ResolvedRecipeYAMLArtifactName, yaml)
			result.ContentSHA256 = sha256Hex(contents)
		}
	} else if phase != "pin" {
		return nil, fmt.Errorf("invalid hook resolution phase")
	}
	commit := contextual.GitCommitContext{PersistHash: head, ParentHash: g.ParentHash}
	if commit.ParentHash == "" {
		commit.ParentHash = head
	}
	var outputArts []jobdb.Artifact
	if recipeArtifact != nil {
		outputArts = append(outputArts, recipeArtifact)
	}
	if pack != nil {
		outputArts = append(outputArts, pack)
	}
	scope := ""
	if g.Workspace != nil {
		scope = g.Workspace.ScopeID
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutputRaw{WorkspaceScopeID: scope, GitResult: commit, Output: result})
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env, outputArts...)
}

type candidateRecipeResolver struct {
	RecipeSourceResolver
	dir, repository, commit string
}

func (r *candidateRecipeResolver) read(p string) ([]byte, error) {
	if _, err := hookpolicy.Path(p); err != nil {
		return nil, err
	}
	// Reject symlinks at every level, including dangling symlinks: only truly
	// absent ordinary files qualify for optional fallback.
	cur := r.dir
	for _, part := range strings.Split(p, "/") {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("hook source may not traverse symlink %s", p)
		}
	}
	return os.ReadFile(cur)
}
func (r *candidateRecipeResolver) LoadYAML(ctx context.Context, project string, res RecipeSourceResolution) ([]byte, error) {
	p, err := parseGitRecipeSelector(res.EffectiveSelector())
	if err == nil && p.RepositoryURL == r.repository && p.Ref == r.commit {
		raw, err := r.read(p.RecipePath)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	if loader, ok := r.RecipeSourceResolver.(RecipeSourceYAMLLoader); ok {
		raw, err := loader.LoadYAML(ctx, project, res)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	rec, err := r.RecipeSourceResolver.Load(ctx, project, res)
	if err != nil {
		return nil, err
	}
	return marshalRecipeYAML(rec)
}

func (d DefaultRecipeExecutor) executeHook(ctx workflow.Context, parent *template.ResolutionContext, n *recipe.NodeInclude) error {
	wrapper, err := prepareHook(ctx, parent, n)
	if err != nil {
		return err
	}
	return d.self().ExecuteNode(ctx, parent, &wrapper)
}

func prepareHook(ctx workflow.Context, parent *template.ResolutionContext, n *recipe.NodeInclude) (recipe.Node, error) {
	hook, err := hookpolicy.Reference(n.Include.Recipe)
	if err != nil {
		return recipe.Node{}, err
	}
	if _, err := compileExpectedOutputs(n.ExpectOutputs); err != nil {
		return recipe.Node{}, err
	}
	if parent.Options.Mode == template.ModeValidate {
		meta := n.NodeMetadata
		meta.ExpectOutputs = nil
		outputs := hookOutputPlaceholder(n)
		if n.Include.IfMissing != nil {
			outputs = n.Include.IfMissing.Outputs
		}
		return recipe.Node{NodeImpl: &recipe.NodeSequence{NodeMetadata: meta, SequenceData: recipe.SequenceData{Outputs: outputs}}}, nil
	}
	if ctx.HookPolicy == nil {
		return recipe.Node{}, fmt.Errorf("hook policy was not captured during job preparation")
	}
	if n.Workspace != nil {
		return recipe.Node{}, fmt.Errorf("put workspace selection on the containing node for cell hooks")
	}
	// Resolve outside the execution retry scope. The durable task result is reused
	// by retries of this invocation; a new visit advances the local invocation ID.
	invocation := parent.NextWorkspaceID("hook:" + template.ScopeID(n.NodeMetadata, "", template.ScopeSequence))
	p := parent.Permissions
	e, _ := ctx.HookPolicy.Select(hook)
	req := workerops.ActivityInvocationRequest{Input: map[string]any{"hook": hook, "policy": e, "perms": n.Perms, "repository": ctx.HookPolicy.Repository, "policy_revision": ctx.HookPolicy.Revision}, Permissions: &p, GitProfile: parent.GitProfile, GitTaskContext: *gitstate.NewGlobalGitTaskContext(parent.TaskExecutionContext())}
	req.Input["source_hooks"] = ctx.SourceHooks
	req.Input["initial_base"] = ctx.HookBase
	var result hookResolution
	var recipeArtifact jobdb.Artifact
	// Pinning has its own durable outcome. A crash during source loading must
	// not perform another floating-ref lookup.
	for _, phase := range []string{"pin", "load"} {
		req.Input["phase"] = phase
		req.GitTaskContext = *gitstate.NewGlobalGitTaskContext(parent.TaskExecutionContext())
		out, err := workflow.ValueCall(ctx.JobContext, "hook:"+invocation+":"+phase, hookResolveTaskType, jobdb.RunPolicy{}, req)
		if err != nil {
			return recipe.Node{}, err
		}
		raw, err := out.GetData()
		if err != nil {
			return recipe.Node{}, err
		}
		decoded, err := decodeTaskOutput(raw)
		if err != nil {
			return recipe.Node{}, err
		}
		if decoded.Kind != coretask.OutputKindActivityInvocationOutput {
			return recipe.Node{}, fmt.Errorf("invalid hook resolution envelope")
		}
		encoded, err := json.Marshal(decoded.Activity.OpOutput)
		if err != nil {
			return recipe.Node{}, err
		}
		if err = json.Unmarshal(encoded, &result); err != nil {
			return recipe.Node{}, err
		}
		if result.Path != hook || !isFullGitHash(result.Candidate) || decoded.Activity.GitResult.PersistHash != result.Candidate {
			return recipe.Node{}, fmt.Errorf("invalid hook resolution result")
		}
		if !isFullGitHash(result.Source.ResolvedCommit) {
			return recipe.Node{}, fmt.Errorf("invalid hook source commit")
		}
		if phase == "load" && result.Source.ResolvedCommit != req.Input["source_commit"] {
			return recipe.Node{}, fmt.Errorf("hook source changed after pinning")
		}
		req.Input["source_commit"] = result.Source.ResolvedCommit
		if phase == "load" && !result.Absent {
			recipeArtifact, err = findResolvedRecipeYAMLArtifact(out, result.RecipeYAMLArtifact)
			if err != nil {
				return recipe.Node{}, err
			}
		}
		parent.UpdateGitState(decoded.Activity.GitResult)
	}
	if result.Absent {
		e = hookpolicy.Empty()
	}
	authority, err := e.Authority(result.Candidate, parent.Permissions, n.Perms)
	if err != nil {
		return recipe.Node{}, err
	}
	authority.ChildPolicy, err = json.Marshal(ctx.HookPolicy)
	if err != nil {
		return recipe.Node{}, err
	}
	var rec recipe.Recipe
	var recipeYAML []byte
	if result.Absent {
		if n.Include.IfMissing == nil {
			return recipe.Node{}, fmt.Errorf("hook %s: %w", hook, os.ErrNotExist)
		}
		rec = recipe.Recipe{RecipeImpl: &recipe.RecipeSequence{RecipeMetadata: recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{ID: "absent_hook"}}, SequenceData: recipe.SequenceData{Outputs: n.Include.IfMissing.Outputs}}}
	} else {
		recipeYAML, err = readResolvedRecipeYAML(ctx.JobContext.Context(), result.RecipeYAMLArtifact, result.RecipeYAML, recipeArtifact)
		if err != nil {
			return recipe.Node{}, err
		}
		loaded, err := recipe.LoadInternalRecipeFromString(recipeYAML)
		if err != nil {
			return recipe.Node{}, err
		}
		rec = *loaded
	}
	loaded := loadedInlineRecipe{Recipe: rec, Snapshot: result.Source, Raw: recipeYAML}
	wrapper, err := inlineWrapperNode(n, rec, loaded, parent.TaskExecutionContext().Invocation.NodePath+"/"+n.ID, "")
	if err != nil {
		return recipe.Node{}, err
	}
	seq := wrapper.NodeImpl.(*recipe.NodeSequence)
	seq.Internal.HookAuthority = &authority
	if !result.Absent {
		parsed, err := parseGitRecipeSelector(result.Source.ResolvedSelector)
		if err != nil {
			return recipe.Node{}, err
		}
		seq.Internal.HookSourceRepo = parsed.RepositoryURL
		seq.Internal.HookSourceRef = parsed.Ref
	}
	if len(rec.GetMetadata().Extensions.Functions) > 0 {
		execCtx := parent.TaskExecutionContext().JobContext()
		execCtx.RecipeSource.Repo = seq.Internal.HookSourceRepo
		execCtx.RecipeSource.Ref = seq.Internal.HookSourceRef
		seq.Internal.CELProvider, err = recipeCELOptionsProvider(rec, execCtx, parent.GetGitCommitContext(), ExecutionOptions{
			ResolvedSelectors: result.Functions.ResolvedSelectors,
			ResolvedGitRefs:   result.Functions.ResolvedGitRefs,
		}, parent.Options.CELOptionsProvider)
		if err != nil {
			return recipe.Node{}, fmt.Errorf("failed to configure recipe extension functions: %w", err)
		}
	}
	seq.Internal.Inline.ContentSHA256 = result.ContentSHA256
	return wrapper, nil
}

func compileExpectedOutputs(schema map[string]any) (*jsonschema.Schema, error) {
	if schema == nil {
		return nil, nil
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	// No network schema loading: contracts are self-contained recipe data.
	c.UseLoader(schemaLoader{})
	if err := c.AddResource("urn:c2j:hook:outputs", schema); err != nil {
		return nil, fmt.Errorf("invalid expect_outputs: %w", err)
	}
	s, err := c.Compile("urn:c2j:hook:outputs")
	if err != nil {
		return nil, fmt.Errorf("invalid expect_outputs: %w", err)
	}
	return s, nil
}
func validateExpectedOutputs(schema map[string]any, outputs map[string]any) error {
	s, err := compileExpectedOutputs(schema)
	if err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	raw, err := json.Marshal(outputs)
	if err != nil {
		return err
	}
	var value any
	if err = json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if err = s.Validate(value); err != nil {
		return fmt.Errorf("hook output contract: %w", err)
	}
	return nil
}

type schemaLoader struct{}

func (schemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not allowed", url)
}

// bindHookIncludeSources preserves each nested include's source context at runtime.
func bindHookIncludeSources(rec recipe.Recipe) error {
	root, _, _, err := includedRecipeRootNode(rec)
	if err != nil {
		return err
	}
	var walk func(recipe.Node) error
	walk = func(node recipe.Node) error {
		switch n := node.NodeImpl.(type) {
		case *recipe.NodeSequence:
			if n.Internal != nil && n.Internal.Inline != nil {
				p, err := parseGitRecipeSelector(n.Internal.Inline.Source.ResolvedSelector)
				if err == nil {
					n.Internal.HookSourceRepo = p.RepositoryURL
					n.Internal.HookSourceRef = p.Ref
				}
			}
			for _, child := range n.Sequence {
				if err := walk(child); err != nil {
					return err
				}
			}
		case *recipe.NodeState:
			if n.States != nil {
				for _, child := range n.States.States {
					if err := walk(child.Node); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return walk(root)
}
