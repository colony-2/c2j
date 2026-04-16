package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	invschema "github.com/invopop/jsonschema"
	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
	yaml "gopkg.in/yaml.v3"
)

// ExtensionOpSpec models the op.yaml configuration for a dynamic extension op.
// This keeps the schema intentionally minimal and practical.
type ExtensionOpSpec struct {
	// Metadata
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Version     string `yaml:"version"`

	// Execution (choose one)
	// Preferred: provide a shell and a run string
	Shell string `yaml:"shell"` // bash, sh, zsh (default: bash if available, fallback sh)
	Run   string `yaml:"run"`   // shell command to execute

	// Alternative: provide argv vector
	Command []string `yaml:"command"` // command argv (first is program)
	Args    []string `yaml:"args"`    // additional args (appended to Command)

	// Working directory and environment
	WorkingDirectory string            `yaml:"working_directory"` // default: op directory
	Env              map[string]string `yaml:"env"`

	// Timeout (Go duration string, e.g., "30s", "5m")
	Timeout string `yaml:"timeout"`

	// Optional schemas (currently not enforced at runtime, reserved for docs/validation)
	InputSchema  map[string]any `yaml:"input_schema"`
	OutputSchema map[string]any `yaml:"output_schema"`
}

// runtimeCtx holds execution-time constants passed into the handler.
type runtimeCtx struct {
	name        string
	projectRoot string
	opDir       string
	spec        ExtensionOpSpec
	// compiled schemas (optional)
	compiledInput  *jsonschemav6.Schema
	compiledOutput *jsonschemav6.Schema
	// retained for docs/introspection
	inputSchemaDoc  *invschema.Schema
	outputSchemaDoc *invschema.Schema
}

// extInputsWrapper is the per-op input wrapper that:
// - supplies JSON Schema for schema generation
// - validates YAML inputs at parse-time via UnmarshalYAML
// - captures arbitrary input keys via yaml:",inline"
type extInputsWrapper struct {
	// Data captures all keys
	Data map[string]interface{} `yaml:",inline" json:"-"`

	// Schema (not marshaled) used for JSON Schema and validation
	schemaDoc      *invschema.Schema    `yaml:"-" json:"-"`
	compiledSchema *jsonschemav6.Schema `yaml:"-" json:"-"`
}

// JSONSchema provides the extension's input JSON Schema to the reflector.
func (w extInputsWrapper) JSONSchema() *invschema.Schema {
	if w.schemaDoc != nil {
		return w.schemaDoc
	}
	// Permissive fallback
	return &invschema.Schema{Type: "object"}
}

// JSONSchemaExtend allows the reflector to apply the provided schema document directly.
func (w extInputsWrapper) JSONSchemaExtend(s *invschema.Schema) {
	if w.schemaDoc != nil {
		// Shallow copy of the schema document into s
		*s = *w.schemaDoc
		return
	}
	s.Type = "object"
}

// UnmarshalYAML validates inputs against the compiled schema if present.
func (w *extInputsWrapper) UnmarshalYAML(n *yaml.Node) error {
	var m map[string]interface{}
	if err := n.Decode(&m); err != nil {
		return err
	}
	if w.compiledSchema != nil {
		if err := w.compiledSchema.Validate(m); err != nil {
			return err
		}
	} else if w.schemaDoc != nil && len(w.schemaDoc.Required) > 0 {
		// Fallback: enforce required keys from schemaDoc when no compiled validator
		for _, req := range w.schemaDoc.Required {
			if _, ok := m[req]; !ok {
				return fmt.Errorf("missing required field: %s", req)
			}
		}
	}
	w.Data = m
	return nil
}

// buildCmd constructs the exec.Cmd based on spec.
func (e *runtimeCtx) buildCmd(ctx context.Context) (*exec.Cmd, error) {
	// Vector form has priority if provided
	if len(e.spec.Command) > 0 {
		argv := append([]string{}, e.spec.Command...)
		if len(e.spec.Args) > 0 {
			argv = append(argv, e.spec.Args...)
		}
		return exec.CommandContext(ctx, argv[0], argv[1:]...), nil
	}

	// Otherwise, use shell+run
	run := strings.TrimSpace(e.spec.Run)
	if run == "" {
		return nil, fmt.Errorf("extension op '%s' has no command: specify either 'command' or 'run'", e.name)
	}

	shell := strings.TrimSpace(e.spec.Shell)
	if shell == "" {
		// Try bash, fallback to sh
		if _, err := exec.LookPath("bash"); err == nil {
			shell = "bash"
		} else {
			shell = "sh"
		}
	}

	// Build shell command
	// Use -lc where possible to support login/profile and compound commands
	// If shell is sh, use -lc only if supported; sh typically supports -c.
	if shell == "bash" || shell == "zsh" {
		return exec.CommandContext(ctx, shell, "-lc", run), nil
	}
	return exec.CommandContext(ctx, shell, "-c", run), nil
}

func (e *runtimeCtx) resolveWorkingDir() string {
	wd := strings.TrimSpace(e.spec.WorkingDirectory)
	if wd == "" {
		return e.opDir
	}
	if filepath.IsAbs(wd) {
		return wd
	}
	// relative to project root if starts with ./ or not
	return filepath.Join(e.projectRoot, wd)
}

func parseDurationOrZero(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// DiscoverAndRegister is the legacy runtime-discovery path for extension ops.
// It looks under .colony2/ops while ascending from startDir to root.
// Selector-backed extension ops are resolved separately and can live anywhere.
func DiscoverAndRegister(startDir string) ([]ops.RegisterableOp, error) {
	opsFound, err := Discover(startDir)
	if err != nil {
		return nil, err
	}
	for _, o := range opsFound {
		ops.Register(o)
	}
	return opsFound, nil
}

// Discover returns legacy runtime-discovered extension ops without registering them globally.
func Discover(startDir string) ([]ops.RegisterableOp, error) {
	projectRoot, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}
	if projectRoot == "" {
		// not found; nothing to do
		return nil, nil
	}

	base := filepath.Join(projectRoot, ".colony2", "ops")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed reading %s: %w", base, err)
	}

	var out []ops.RegisterableOp
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		opDir := filepath.Join(base, name)
		// Try both op.yaml and op.yml
		specPath := filepath.Join(opDir, "op.yaml")
		if _, err := os.Stat(specPath); os.IsNotExist(err) {
			alt := filepath.Join(opDir, "op.yml")
			if _, err2 := os.Stat(alt); err2 == nil {
				specPath = alt
			} else {
				continue
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

		opName := strings.TrimSpace(spec.Name)
		if opName == "" {
			opName = name // default from directory name
		}
		md := ops.OpMetadata{
			Type:             opName,
			Description:      orDefault(spec.Description, fmt.Sprintf("extension op '%s'", opName)),
			Version:          orDefault(spec.Version, "0.1.0"),
			AcceptsArtifacts: true,
		}
		if d, err := parseDurationOrZero(spec.Timeout); err == nil && d > 0 {
			md.DefaultTimeout = d
		} else {
			md.DefaultTimeout = 5 * time.Minute
		}

		rctx := &runtimeCtx{
			name:        opName,
			projectRoot: projectRoot,
			opDir:       opDir,
			spec:        spec,
		}
		// Parse/compile schemas if provided (best effort)
		if is := spec.InputSchema; is != nil {
			if doc, compiled, err := parseSchema(is); err == nil {
				rctx.inputSchemaDoc = doc
				rctx.compiledInput = compiled
			}
		}
		if oschema := spec.OutputSchema; oschema != nil {
			if doc, compiled, err := parseSchema(oschema); err == nil {
				rctx.outputSchemaDoc = doc
				rctx.compiledOutput = compiled
			}
		}
		// Build a RegisterableOp using the provider constructor to supply a
		// per-op input wrapper instance with JSON Schema + YAML validation.
		op := ops.NewActivityMappedOpWithProviderV2[map[string]interface{}, map[string]interface{}](md, func(_ ops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			// Optional input validation
			if rctx.compiledInput != nil {
				if err := rctx.compiledInput.Validate(input); err != nil {
					return nil, fmt.Errorf("extension op '%s' input failed schema validation: %w", rctx.name, err)
				}
			}

			// Determine timeout from spec, if any
			var cancel context.CancelFunc
			if d, err := parseDurationOrZero(rctx.spec.Timeout); err == nil && d > 0 {
				ctx, cancel = context.WithTimeout(ctx, d)
				defer cancel()
			}

			inJSON, err := json.Marshal(input)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal input: %w", err)
			}

			cmd, err := rctx.buildCmd(ctx)
			if err != nil {
				return nil, err
			}
			cmd.Dir = rctx.resolveWorkingDir()

			// Env setup
			cmd.Env = os.Environ()
			for k, v := range rctx.spec.Env {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
			}
			cmd.Env = append(cmd.Env,
				fmt.Sprintf("VIBETHIS_PROJECT_ROOT=%s", rctx.projectRoot),
				fmt.Sprintf("VIBETHIS_OP_DIR=%s", rctx.opDir),
				fmt.Sprintf("VIBETHIS_OP_NAME=%s", rctx.name),
				fmt.Sprintf("VIBETHIS_INPUT_JSON=%s", string(inJSON)),
			)

			// stdio
			cmd.Stdin = bytes.NewReader(inJSON)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("extension op '%s' failed: %w; stderr: %s", rctx.name, err, strings.TrimSpace(stderr.String()))
			}

			outMap := make(map[string]interface{})
			dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
			dec.UseNumber()
			if err := dec.Decode(&outMap); err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("extension op '%s' produced invalid JSON on stdout: %w; raw: %s", rctx.name, err, strings.TrimSpace(stdout.String()))
			}
			if rctx.compiledOutput != nil {
				if vErr := rctx.compiledOutput.Validate(outMap); vErr != nil {
					return nil, fmt.Errorf("extension op '%s' output failed schema validation: %w", rctx.name, vErr)
				}
			}
			return outMap, nil
		}, func() interface{} {
			return &extInputsWrapper{
				schemaDoc:      rctx.inputSchemaDoc,
				compiledSchema: rctx.compiledInput,
			}
		})
		out = append(out, op)
	}
	return out, nil
}

func findProjectRoot(startDir string) (string, error) {
	if startDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = wd
	}

	// If env var is provided, prefer it
	if pr := strings.TrimSpace(os.Getenv("VIBETHIS_PROJECT_ROOT")); pr != "" {
		if _, err := os.Stat(filepath.Join(pr, ".colony2")); err == nil {
			return pr, nil
		}
	}

	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".colony2", "ops")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached root
			break
		}
		dir = parent
	}
	return "", nil
}

func orDefault[T ~string](v T, d T) T {
	if strings.TrimSpace(string(v)) == "" {
		return d
	}
	return v
}

// parseSchema converts a YAML-parsed schema (map[string]any) into both an
// invopop schema (for documentation) and a compiled jsonschema/v6 validator.
func parseSchema(m map[string]any) (*invschema.Schema, *jsonschemav6.Schema, error) {
	// Marshal to JSON then unmarshal into invopop and compile with v6
	b, err := json.Marshal(m)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal schema: %w", err)
	}
	// invopop schema doc (best-effort)
	var doc invschema.Schema
	if err := json.Unmarshal(b, &doc); err != nil {
		// continue without doc if malformed
		doc = invschema.Schema{}
	}
	// Ensure 'required' is captured even if unmarshalling missed it
	if rv, ok := m["required"]; ok {
		switch rr := rv.(type) {
		case []any:
			var req []string
			for _, it := range rr {
				if s, ok := it.(string); ok {
					req = append(req, s)
				}
			}
			if len(req) > 0 {
				doc.Required = req
			}
		case []string:
			if len(rr) > 0 {
				doc.Required = rr
			}
		}
	}
	comp := jsonschemav6.NewCompiler()
	comp.DefaultDraft(jsonschemav6.Draft2020)
	if err := comp.AddResource("inmem://op-schema.json", bytes.NewReader(b)); err != nil {
		return &doc, nil, fmt.Errorf("add resource: %w", err)
	}
	compiled, err := comp.Compile("inmem://op-schema.json")
	if err != nil {
		return &doc, nil, fmt.Errorf("compile schema: %w", err)
	}
	return &doc, compiled, nil
}

// Note: We purposely do not implement ops.RegisterableOp directly here,
// because that interface uses a sealed pattern. We construct it via
// ops.NewActivityMappedOpV2 below to maintain compatibility.
