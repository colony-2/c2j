package extensionfuncs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
	yaml "gopkg.in/yaml.v3"
)

const (
	FunctionModeJSON     = "json"
	FunctionModeFunction = "function"
)

var celIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type BuildOptions struct {
	BaseDir          string
	RepositorySource string
	RepositoryRef    string
}

type PackageManifest struct {
	Name             string                `yaml:"name"`
	Description      string                `yaml:"description"`
	Version          string                `yaml:"version"`
	Shell            string                `yaml:"shell"`
	Timeout          string                `yaml:"timeout"`
	WorkingDirectory string                `yaml:"working_directory"`
	Env              map[string]string     `yaml:"env"`
	Functions        []PackageFunctionSpec `yaml:"functions"`
}

type PackageFunctionSpec struct {
	Name        string             `yaml:"name"`
	Description string             `yaml:"description"`
	Mode        string             `yaml:"mode"`
	Execution   string             `yaml:"execution"`
	Args        []FunctionArgSpec  `yaml:"args"`
	Returns     FunctionReturnSpec `yaml:"returns"`
}

type FunctionArgSpec struct {
	Name   string         `yaml:"name"`
	Schema map[string]any `yaml:"schema"`
}

type FunctionReturnSpec struct {
	Schema map[string]any `yaml:"schema"`
}

type loadedPackage struct {
	resolved        *extops.ResolvedSelectorPath
	manifestPath    string
	manifest        PackageManifest
	timeout         time.Duration
	workingDir      string
	functionsByName map[string]*loadedFunction
}

type loadedFunction struct {
	pkg          *loadedPackage
	spec         PackageFunctionSpec
	argSchemas   []*compiledSchema
	returnSchema *compiledSchema
	memoMu       sync.Mutex
	memo         map[string]any
}

type compiledSchema struct {
	raw      map[string]any
	compiled *jsonschemav6.Schema
}

func BuildProvider(ctx context.Context, imports []recipe.ExtensionFunctionImport, opts BuildOptions) (*funcregistry.Builder, error) {
	if len(imports) == 0 {
		return nil, nil
	}

	builder := funcregistry.NewBuilder()
	exposed := map[string]string{}
	for _, imp := range imports {
		pkg, err := loadPackage(ctx, imp.Selector, opts)
		if err != nil {
			return nil, err
		}

		importedFunctions, err := pkg.importedFunctions(imp)
		if err != nil {
			return nil, err
		}
		for _, imported := range importedFunctions {
			if prior, ok := exposed[imported.exposedName]; ok {
				return nil, fmt.Errorf("extension function name collision for %q between %s and %s", imported.exposedName, prior, imp.Selector)
			}
			exposed[imported.exposedName] = imp.Selector
			builder.WithBuiltin(imported.exposedName, imported.factory())
		}
	}
	return builder, nil
}

type importedFunction struct {
	exposedName string
	loaded      *loadedFunction
}

func loadPackage(ctx context.Context, selector string, opts BuildOptions) (*loadedPackage, error) {
	resolved, err := extops.ResolvePath(ctx, selector, extops.ResolveOptions{
		BaseDir:          strings.TrimSpace(opts.BaseDir),
		RepositorySource: strings.TrimSpace(opts.RepositorySource),
		RepositoryRef:    strings.TrimSpace(opts.RepositoryRef),
	})
	if err != nil {
		return nil, err
	}

	manifestPath, err := locateManifestPath(resolved.Dir)
	if err != nil {
		return nil, err
	}

	buf, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", manifestPath, err)
	}

	var manifest PackageManifest
	if err := yaml.Unmarshal(buf, &manifest); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		manifest.Name = filepath.Base(resolved.Dir)
	}
	if manifest.Env == nil {
		manifest.Env = map[string]string{}
	}

	timeout, err := parseDurationOrZero(manifest.Timeout)
	if err != nil {
		return nil, fmt.Errorf("invalid timeout in %s: %w", manifestPath, err)
	}

	pkg := &loadedPackage{
		resolved:        resolved,
		manifestPath:    manifestPath,
		manifest:        manifest,
		timeout:         timeout,
		workingDir:      resolveWorkingDir(resolved.Dir, manifest.WorkingDirectory),
		functionsByName: map[string]*loadedFunction{},
	}
	if err := pkg.validateAndCompile(); err != nil {
		return nil, err
	}
	return pkg, nil
}

func locateManifestPath(dir string) (string, error) {
	for _, name := range []string{"functions.yaml", "functions.yml"} {
		path := filepath.Join(dir, name)
		if stat, err := os.Stat(path); err == nil && !stat.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("extension function package %q missing functions.yaml", dir)
}

func (p *loadedPackage) validateAndCompile() error {
	if len(p.manifest.Functions) == 0 {
		return fmt.Errorf("extension function package %q exports no functions", p.manifestPath)
	}
	seen := map[string]struct{}{}
	for _, spec := range p.manifest.Functions {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return fmt.Errorf("extension function package %q contains a function with an empty name", p.manifestPath)
		}
		if !celIdentifierPattern.MatchString(name) {
			return fmt.Errorf("extension function %q in %s is not a valid CEL identifier", name, p.manifestPath)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("extension function package %q exports duplicate function %q", p.manifestPath, name)
		}
		seen[name] = struct{}{}

		mode := strings.TrimSpace(spec.Mode)
		if mode == "" {
			mode = FunctionModeJSON
		}
		switch mode {
		case FunctionModeJSON, FunctionModeFunction:
		default:
			return fmt.Errorf("extension function %q in %s has unsupported mode %q", name, p.manifestPath, spec.Mode)
		}
		if strings.TrimSpace(spec.Execution) == "" {
			return fmt.Errorf("extension function %q in %s is missing execution", name, p.manifestPath)
		}
		if len(spec.Args) == 0 && mode == FunctionModeFunction {
			return fmt.Errorf("extension function %q in %s mode %q requires exactly one arg", name, p.manifestPath, mode)
		}

		argSchemas := make([]*compiledSchema, 0, len(spec.Args))
		for i, arg := range spec.Args {
			schema, err := compileSchema(arg.Schema)
			if err != nil {
				return fmt.Errorf("extension function %q arg %d schema in %s: %w", name, i, p.manifestPath, err)
			}
			argSchemas = append(argSchemas, schema)
		}
		returnSchema, err := compileSchema(spec.Returns.Schema)
		if err != nil {
			return fmt.Errorf("extension function %q return schema in %s: %w", name, p.manifestPath, err)
		}

		if mode == FunctionModeFunction {
			if len(argSchemas) != 1 {
				return fmt.Errorf("extension function %q in %s mode %q requires exactly one arg", name, p.manifestPath, mode)
			}
			if !isFunctionModeScalarSchema(argSchemas[0].raw) {
				return fmt.Errorf("extension function %q in %s mode %q requires a scalar input schema", name, p.manifestPath, mode)
			}
			if !isFunctionModeScalarSchema(returnSchema.raw) {
				return fmt.Errorf("extension function %q in %s mode %q requires a scalar return schema", name, p.manifestPath, mode)
			}
		}

		spec.Mode = mode
		p.functionsByName[name] = &loadedFunction{
			pkg:          p,
			spec:         spec,
			argSchemas:   argSchemas,
			returnSchema: returnSchema,
			memo:         map[string]any{},
		}
	}
	return nil
}

func (p *loadedPackage) importedFunctions(imp recipe.ExtensionFunctionImport) ([]importedFunction, error) {
	selected := map[string]struct{}{}
	if len(imp.Include) == 0 {
		for _, spec := range p.manifest.Functions {
			selected[spec.Name] = struct{}{}
		}
	} else {
		for _, name := range imp.Include {
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, fmt.Errorf("extension import %q contains an empty include name", imp.Selector)
			}
			if _, ok := p.functionsByName[name]; !ok {
				return nil, fmt.Errorf("extension import %q includes unknown function %q", imp.Selector, name)
			}
			selected[name] = struct{}{}
		}
	}

	for from, to := range imp.Rename {
		if _, ok := p.functionsByName[from]; !ok {
			return nil, fmt.Errorf("extension import %q renames unknown function %q", imp.Selector, from)
		}
		if _, ok := selected[from]; !ok {
			return nil, fmt.Errorf("extension import %q renames function %q that is not included", imp.Selector, from)
		}
		if !celIdentifierPattern.MatchString(strings.TrimSpace(to)) {
			return nil, fmt.Errorf("extension import %q renames function %q to invalid CEL identifier %q", imp.Selector, from, to)
		}
	}

	result := make([]importedFunction, 0, len(selected))
	exposed := map[string]struct{}{}
	for _, spec := range p.manifest.Functions {
		loaded := p.functionsByName[spec.Name]
		if _, ok := selected[spec.Name]; !ok {
			continue
		}
		exposedName := spec.Name
		if renamed, ok := imp.Rename[spec.Name]; ok {
			exposedName = strings.TrimSpace(renamed)
		}
		if _, ok := exposed[exposedName]; ok {
			return nil, fmt.Errorf("extension import %q exposes duplicate function name %q", imp.Selector, exposedName)
		}
		exposed[exposedName] = struct{}{}
		result = append(result, importedFunction{exposedName: exposedName, loaded: loaded})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("extension import %q exposes no functions", imp.Selector)
	}
	return result, nil
}

func (i importedFunction) factory() funcregistry.BuiltinFactory {
	return func(adapter celtypes.Adapter, _ funcregistry.ContextProvider) cel.EnvOption {
		argTypes := make([]*cel.Type, 0, len(i.loaded.argSchemas))
		for _, arg := range i.loaded.argSchemas {
			argTypes = append(argTypes, celTypeForSchema(arg.raw))
		}
		return cel.Function(
			i.exposedName,
			cel.Overload(
				sanitizeOverloadName(i.exposedName)+"_extension",
				argTypes,
				celTypeForSchema(i.loaded.returnSchema.raw),
				cel.FunctionBinding(func(values ...ref.Val) ref.Val {
					out, err := i.loaded.invoke(values)
					if err != nil {
						return celtypes.NewErr("%s: %v", i.exposedName, err)
					}
					return adapter.NativeToValue(out)
				}),
			),
		)
	}
}

func (f *loadedFunction) invoke(values []ref.Val) (any, error) {
	if len(values) != len(f.argSchemas) {
		return nil, fmt.Errorf("expected %d args, got %d", len(f.argSchemas), len(values))
	}

	args := make([]any, 0, len(values))
	for i, value := range values {
		arg, err := nativeValueFromCEL(value)
		if err != nil {
			return nil, fmt.Errorf("decode arg %d: %w", i, err)
		}
		if err := f.argSchemas[i].validate(arg); err != nil {
			return nil, fmt.Errorf("arg %d failed schema validation: %w", i, err)
		}
		args = append(args, arg)
	}

	cacheKey, err := memoizationKey(f.pkg.resolved, f.spec.Name, args)
	if err != nil {
		return nil, err
	}

	f.memoMu.Lock()
	if cached, ok := f.memo[cacheKey]; ok {
		f.memoMu.Unlock()
		return cloneJSONValue(cached)
	}
	f.memoMu.Unlock()

	out, err := f.execute(args)
	if err != nil {
		return nil, err
	}
	if err := f.returnSchema.validate(out); err != nil {
		return nil, fmt.Errorf("output failed schema validation: %w", err)
	}

	f.memoMu.Lock()
	f.memo[cacheKey] = out
	f.memoMu.Unlock()
	return cloneJSONValue(out)
}

func (f *loadedFunction) execute(args []any) (any, error) {
	stdin, err := f.buildStdin(args)
	if err != nil {
		return nil, err
	}

	runCtx := context.Background()
	if f.pkg.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, f.pkg.timeout)
		defer cancel()
	}

	stdout, stderr, err := extops.ExecuteProcess(runCtx, extops.RunRequest{
		WorkspaceRoot: f.pkg.resolved.ProjectRoot,
		WorkingDir:    f.pkg.workingDir,
		Shell:         strings.TrimSpace(f.pkg.manifest.Shell),
		Run:           strings.TrimSpace(f.spec.Execution),
		Env:           f.pkg.manifest.Env,
		Stdin:         stdin,
	})
	if err != nil {
		return nil, fmt.Errorf("execute %q: %w; stderr: %s", f.spec.Name, err, strings.TrimSpace(string(stderr)))
	}

	switch f.spec.Mode {
	case FunctionModeFunction:
		return decodeFunctionModeOutput(stdout, f.returnSchema.raw)
	default:
		return decodeJSONModeOutput(stdout)
	}
}

func (f *loadedFunction) buildStdin(args []any) ([]byte, error) {
	switch f.spec.Mode {
	case FunctionModeFunction:
		return encodeFunctionModeInput(args[0], f.argSchemas[0].raw)
	default:
		return json.Marshal(map[string]any{"args": args})
	}
}

func compileSchema(raw map[string]any) (*compiledSchema, error) {
	if len(raw) == 0 {
		return &compiledSchema{}, nil
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	compiler := jsonschemav6.NewCompiler()
	compiler.DefaultDraft(jsonschemav6.Draft2020)
	var doc any
	if err := json.Unmarshal(buf, &doc); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	if err := compiler.AddResource("inmem://schema.json", doc); err != nil {
		return nil, fmt.Errorf("add schema resource: %w", err)
	}
	compiled, err := compiler.Compile("inmem://schema.json")
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return &compiledSchema{raw: raw, compiled: compiled}, nil
}

func (c *compiledSchema) validate(value any) error {
	if c == nil || c.compiled == nil {
		return nil
	}
	return c.compiled.Validate(value)
}

func nativeValueFromCEL(value ref.Val) (any, error) {
	if value == nil {
		return nil, nil
	}
	raw := value.Value()
	switch raw.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return raw, nil
	}
	return cloneJSONValue(raw)
}

func memoizationKey(resolved *extops.ResolvedSelectorPath, functionName string, args []any) (string, error) {
	buf, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal args for memoization: %w", err)
	}
	resolvedSelector := ""
	if resolved != nil {
		if strings.TrimSpace(resolved.ResolvedSelector) != "" {
			resolvedSelector = strings.TrimSpace(resolved.ResolvedSelector)
		} else {
			resolvedSelector = strings.TrimSpace(resolved.Selector)
		}
	}
	return resolvedSelector + "\n" + functionName + "\n" + string(buf), nil
}

func cloneJSONValue(value any) (any, error) {
	buf, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal value: %w", err)
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode value: %w", err)
	}
	return normalizeNumbers(out), nil
}

func decodeJSONModeOutput(stdout []byte) (any, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("missing JSON output")
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON output: %w", err)
	}
	result, ok := raw["result"]
	if !ok {
		return nil, fmt.Errorf("missing result field in JSON output")
	}
	return normalizeNumbers(result), nil
}

func encodeFunctionModeInput(value any, schema map[string]any) ([]byte, error) {
	switch schemaScalarType(schema) {
	case "string":
		str, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("expected string input, got %T", value)
		}
		return []byte(str), nil
	case "integer":
		n, err := toInt64(value)
		if err != nil {
			return nil, err
		}
		return []byte(strconv.FormatInt(n, 10)), nil
	case "number":
		n, err := toFloat64(value)
		if err != nil {
			return nil, err
		}
		return []byte(strconv.FormatFloat(n, 'f', -1, 64)), nil
	case "boolean":
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("expected boolean input, got %T", value)
		}
		return []byte(strconv.FormatBool(b)), nil
	default:
		return nil, fmt.Errorf("unsupported function mode input schema")
	}
}

func decodeFunctionModeOutput(stdout []byte, schema map[string]any) (any, error) {
	switch schemaScalarType(schema) {
	case "string":
		return trimTrailingLineBreak(stdout), nil
	case "integer":
		return strconv.ParseInt(strings.TrimSpace(string(stdout)), 10, 64)
	case "number":
		return strconv.ParseFloat(strings.TrimSpace(string(stdout)), 64)
	case "boolean":
		return strconv.ParseBool(strings.TrimSpace(string(stdout)))
	default:
		return nil, fmt.Errorf("unsupported function mode output schema")
	}
}

func trimTrailingLineBreak(in []byte) string {
	s := string(in)
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s
}

func isFunctionModeScalarSchema(schema map[string]any) bool {
	switch schemaScalarType(schema) {
	case "string", "integer", "number", "boolean":
		return true
	default:
		return false
	}
}

func schemaScalarType(schema map[string]any) string {
	typeValue := schemaTypeValue(schema)
	switch typeValue {
	case "string", "integer", "number", "boolean":
		return typeValue
	default:
		return ""
	}
}

func schemaTypeValue(schema map[string]any) string {
	if len(schema) == 0 {
		return ""
	}
	switch typeValue := schema["type"].(type) {
	case string:
		return typeValue
	case []any:
		for _, value := range typeValue {
			if s, ok := value.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

func celTypeForSchema(schema map[string]any) *cel.Type {
	switch schemaTypeValue(schema) {
	case "string":
		return cel.StringType
	case "integer":
		return cel.IntType
	case "number":
		return cel.DoubleType
	case "boolean":
		return cel.BoolType
	case "array":
		items, _ := schema["items"].(map[string]any)
		if len(items) == 0 {
			return cel.ListType(cel.DynType)
		}
		return cel.ListType(celTypeForSchema(items))
	case "object":
		additional, _ := schema["additionalProperties"].(map[string]any)
		if len(additional) == 0 {
			return cel.MapType(cel.StringType, cel.DynType)
		}
		return cel.MapType(cel.StringType, celTypeForSchema(additional))
	default:
		return cel.DynType
	}
}

func normalizeNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i
		}
		if f, err := typed.Float64(); err == nil {
			return f
		}
		return typed.String()
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeNumbers(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeNumbers(item)
		}
		return out
	default:
		return value
	}
}

func toInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	default:
		return 0, fmt.Errorf("expected integer value, got %T", value)
	}
}

func toFloat64(value any) (float64, error) {
	switch typed := value.(type) {
	case float32:
		return float64(typed), nil
	case float64:
		return typed, nil
	case int:
		return float64(typed), nil
	case int8:
		return float64(typed), nil
	case int16:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case uint:
		return float64(typed), nil
	case uint8:
		return float64(typed), nil
	case uint16:
		return float64(typed), nil
	case uint32:
		return float64(typed), nil
	case uint64:
		return float64(typed), nil
	case json.Number:
		return typed.Float64()
	default:
		return 0, fmt.Errorf("expected numeric value, got %T", value)
	}
}

func resolveWorkingDir(packageDir string, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return packageDir
	}
	if filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(packageDir, configured)
}

func parseDurationOrZero(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	return time.ParseDuration(raw)
}

func sanitizeOverloadName(name string) string {
	var out strings.Builder
	for _, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z':
			out.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			out.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			out.WriteRune(ch)
		default:
			out.WriteByte('_')
		}
	}
	if out.Len() == 0 {
		return "extension"
	}
	return out.String()
}
