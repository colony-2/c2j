package toolenv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/ops/process"
)

const pnpmSchema = "pnpm-private-builds-v2"
const pnpmPolicy = "dangerouslyAllowAllBuilds: true\nstrictDepBuilds: true\nignoreScripts: false\noptional: true\n"

// Installation authority comes from the declaration's source and the package
// workflow. This private policy never changes user, consumer or provider config.
type pnpmTool struct {
	Schema     string         `json:"schema"`
	Diagnostic pnpmDiagnostic `json:"diagnostic"`
	Files      []fileEvidence `json:"files"`
	Native     *pnpmNative    `json:"native,omitempty"`
}
type pnpmDiagnostic struct {
	Bootstrap   string `json:"bootstrap,omitempty"`
	Version     string `json:"version,omitempty"`
	Launcher    string `json:"launcher,omitempty"`
	Node        string `json:"node,omitempty"`
	NodeVersion string `json:"node_version,omitempty"`
	Platform    string `json:"platform"`
	Machine     string `json:"machine,omitempty"`
	Libc        string `json:"libc,omitempty"`
	Policy      string `json:"policy"`
	Validation  string `json:"validation"`
}

func packageIdentity(manager, ref string) string {
	version := "v1"
	if manager == "nix" {
		version = "nix-v2"
	}
	if manager == "pnpm" {
		version = pnpmSchema
	}
	return key([]string{version, runtime.GOOS, runtime.GOARCH, ref})
}
func hasPnpm(tools []Tool) bool {
	for _, t := range tools {
		if strings.HasPrefix(t.Reference, "pnpm:") {
			return true
		}
	}
	return false
}

var credentialURL = regexp.MustCompile(`(https?://)[^\s/@]+:[^\s/@]+@`)

// Raw config output is never attached to diagnostics. Even installation failures
// can echo registry URLs or credentials; retain only bounded, redacted causes.
func pnpmRedact(s string, secrets []string) string {
	for _, value := range secrets {
		if len(value) >= 4 {
			s = strings.ReplaceAll(s, value, "[redacted]")
		}
	}
	s = credentialURL.ReplaceAllString(s, "${1}[redacted]@")
	if len(s) > 4096 {
		s = s[len(s)-4096:]
	}
	return strings.TrimSpace(s)
}
func pnpmConfigKey(k string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(k))
}
func pnpmSecrets(config map[string]any) []string {
	var values []string
	for _, entry := range os.Environ() {
		k, v, _ := strings.Cut(entry, "=")
		k = strings.ToLower(k)
		if strings.Contains(k, "token") || strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "auth") {
			values = append(values, v)
		}
	}
	for k, v := range config {
		if strings.Contains(strings.ToLower(k), "auth") || strings.Contains(strings.ToLower(k), "password") {
			if s, ok := v.(string); ok {
				values = append(values, s)
			}
		}
	}
	return values
}
func configBoolean(v any, want bool) bool {
	if b, ok := v.(bool); ok {
		return b == want
	}
	if s, ok := v.(string); ok {
		return s == strconv.FormatBool(want)
	}
	return false
}
func configPresent(v any) bool { return v != nil && v != "" }

func checkPnpmConfig(config map[string]any, effective bool) error {
	required := map[string]bool{"dangerouslyallowallbuilds": true, "strictdepbuilds": true, "ignorescripts": false, "optional": true}
	seen := map[string]bool{}
	for k, v := range config {
		normalized := pnpmConfigKey(k)
		if want, ok := required[normalized]; ok {
			if !configBoolean(v, want) {
				return fmt.Errorf("conflicting pnpm setting %s", normalized)
			}
			seen[normalized] = true
		}
		// These restrict or override the selected dependency tree/build policy.
		switch normalized {
		case "allowbuilds", "permissions", "onlybuiltdependencies", "onlybuiltdependenciesfile", "neverbuiltdependencies", "ignoredbuiltdependencies", "supportedarchitectures", "arch", "targetarch", "platform", "targetplatform", "libc":
			if configPresent(v) {
				return fmt.Errorf("conflicting pnpm setting %s", normalized)
			}
		}
	}
	if effective {
		for k := range required {
			if !seen[k] {
				return fmt.Errorf("bootstrap did not expose effective pnpm setting %s", k)
			}
		}
	}
	return nil
}

func supportedPnpm(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	_, e3 := strconv.Atoi(parts[2])
	// dangerouslyAllowAllBuilds was introduced in 10.9. Actual build behavior is
	// also exercised by the real-manager verification suite; version is no proof
	// that a local installation or its optional native payload is complete.
	return e1 == nil && e2 == nil && e3 == nil && (major > 10 || (major == 10 && minor >= 9))
}

func preparePnpm(ctx context.Context, dir, spec string, t *Tool) (err error) {
	t.Pnpm = &pnpmTool{Schema: pnpmSchema, Diagnostic: pnpmDiagnostic{Platform: runtime.GOOS + "/" + runtime.GOARCH, Policy: pnpmPolicy, Validation: "failed"}}
	d := &t.Pnpm.Diagnostic
	defer func() {
		if err != nil {
			err = fmt.Errorf("prepare %s: %w", t.Reference, err)
		}
	}()
	bootstrap, err := exec.LookPath("pnpm")
	if err != nil {
		return fmt.Errorf("pnpm bootstrap unavailable: %w", err)
	}
	d.Bootstrap, err = filepath.Abs(bootstrap)
	if err != nil {
		return err
	}
	secrets := pnpmSecrets(nil)
	// A declaration owns this private cache, not the bootstrap's shared store.
	// pnpm 10 and 12 use different environment prefixes; both point locally.
	installEnv := map[string]string{"CI": "true"}
	for _, name := range []string{"PNPM_CONFIG_STORE_DIR", "npm_config_store_dir"} {
		installEnv[name] = filepath.Join(dir, "store")
	}
	for _, name := range []string{"PNPM_CONFIG_STATE_DIR", "npm_config_state_dir"} {
		installEnv[name] = filepath.Join(dir, "state")
	}

	run := func(args ...string) ([]byte, error) {
		out, stderr, e := process.ExecuteProcess(ctx, process.RunRequest{WorkingDir: dir, Command: args, Env: installEnv})
		if e != nil {
			return nil, fmt.Errorf("pnpm %s failed: %w: %s", args[1], e, pnpmRedact(string(stderr), secrets))
		}
		return out, nil
	}
	version, err := run(d.Bootstrap, "--version")
	if err != nil {
		return err
	}
	d.Version = pnpmRedact(strings.TrimSpace(string(version)), secrets)
	if len(d.Version) > 128 {
		d.Version = "invalid version output"
	}
	if !supportedPnpm(d.Version) {
		return fmt.Errorf("unsupported pnpm bootstrap %q: requires private build-policy support (10.9 or later)", d.Version)
	}
	header, err := os.Open(d.Bootstrap)
	if err != nil {
		return err
	}
	prefix := make([]byte, 256)
	n, _ := header.Read(prefix)
	header.Close()
	switch {
	case n >= 4 && string(prefix[:4]) == "\x7fELF":
		d.Launcher = "native ELF"
	default:
		d.Launcher = "script launcher"
	}
	if _, nodeErr := exec.LookPath("node"); nodeErr == nil || d.Launcher != "native ELF" {
		// Capture Node selected for lifecycle scripts by the inherited PATH.
		b, e := run("node", "-p", `JSON.stringify({path:process.execPath,version:process.version})`)
		if e != nil {
			return fmt.Errorf("bootstrap Node unavailable: %w", e)
		}
		var node struct {
			Path    string `json:"path"`
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &node) != nil || node.Path == "" || node.Version == "" {
			return fmt.Errorf("bootstrap Node identity unavailable")
		}
		d.Node, d.NodeVersion = node.Path, node.Version
	}
	readConfig := func(effective bool) error {
		b, e := run(d.Bootstrap, "config", "list", "--json")
		if e != nil {
			return fmt.Errorf("cannot inspect private pnpm policy (config output withheld)")
		}
		var config map[string]any
		if len(b) > 4<<20 || json.Unmarshal(b, &config) != nil || config == nil {
			return fmt.Errorf("bootstrap returned invalid pnpm configuration")
		}
		secrets = append(secrets, pnpmSecrets(config)...)
		return checkPnpmConfig(config, effective)
	}
	// Check ambient denials BEFORE local values could mask them.
	ambient := map[string]any{}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		lower := strings.ToLower(name)
		for _, prefix := range []string{"pnpm_config_", "npm_config_"} {
			if strings.HasPrefix(lower, prefix) {
				ambient[name[len(prefix):]] = value
			}
		}
	}
	if err = checkPnpmConfig(ambient, false); err != nil {
		return err
	}
	if err = readConfig(false); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"private":true}`), 0600); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte(pnpmPolicy), 0600); err != nil {
		return err
	}
	if err = readConfig(true); err != nil {
		return err
	}
	if spec == "pnpm@12.10.1" {
		platform, e := pnpmNativePlatform(runtime.GOOS, runtime.GOARCH)
		if e != nil {
			return e
		}
		machine, e := run("uname", "-m")
		if e != nil {
			return e
		}
		d.Machine = strings.TrimSpace(string(machine))
		libc, e := run("getconf", "GNU_LIBC_VERSION")
		if e != nil {
			return fmt.Errorf("native glibc identity unavailable: %w", e)
		}
		d.Libc = strings.TrimSpace(string(libc))
		if d.Machine != platform.Machine || !strings.HasPrefix(d.Libc, "glibc ") {
			return fmt.Errorf("unsupported native host %q / %q", d.Machine, d.Libc)
		}
	}
	if _, err = run(d.Bootstrap, "add", "--workspace-root", "--save-exact", "--", spec); err != nil {
		return err
	}
	if spec == "pnpm@12.10.1" {
		if err = validatePnpmNative(ctx, dir, t); err != nil {
			return err
		}
	}
	d.Validation = "validated"
	return nil
}

func pnpmPaths(t Tool) []string {
	dir := filepath.Dir(filepath.Dir(t.Bin))
	paths := []string{filepath.Join(dir, "package.json"), filepath.Join(dir, "pnpm-workspace.yaml")}
	for _, name := range t.Executables {
		paths = append(paths, filepath.Join(t.Bin, name))
	}
	if t.nativePnpm() {
		wrapper := filepath.Join(dir, "node_modules", "pnpm")
		paths = append(paths, filepath.Join(wrapper, "package.json"))
		for _, name := range []string{"pnpm", "pn", "pnpx", "pnx"} {
			paths = append(paths, filepath.Join(wrapper, name))
		}
		// pnpm's isolated linker places dependencies beside the wrapper. Native
		// validation retains the resolved directory for the selected architecture.
		if t.Pnpm.Native != nil {
			native := t.Pnpm.Native.Directory
			paths = append(paths, filepath.Join(native, "package.json"), filepath.Join(native, "pnpm"))
		}
	}
	sort.Strings(paths)
	return paths
}
func (t Tool) nativePnpm() bool { return t.Reference == "pnpm:pnpm@12.10.1" }
func retainPnpm(t *Tool) error {
	if t.Pnpm == nil || len(t.Executables) == 0 {
		return fmt.Errorf("pnpm installation exports no executables")
	}
	var err error
	t.Pnpm.Files, err = recordFiles(pnpmPaths(*t))
	return err
}
func pnpmReady(t Tool) bool {
	if t.Pnpm == nil || t.Pnpm.Schema != pnpmSchema || t.Pnpm.Diagnostic.Validation != "validated" || t.Pnpm.Diagnostic.Policy != pnpmPolicy || len(t.Executables) == 0 || t.Identity != packageIdentity("pnpm", t.Reference) {
		return false
	}
	if t.nativePnpm() && !nativePnpmEvidenceReady(t) {
		return false
	}
	policy, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(t.Bin)), "pnpm-workspace.yaml"))
	if err != nil || string(policy) != pnpmPolicy {
		return false
	}
	if !filesReady(t.Pnpm.Files, pnpmPaths(t)) {
		return false
	}
	// Do not accept modified on-disk evidence through an older serialized env.
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(t.Bin)), "ready.json"))
	if err != nil {
		return false
	}
	var disk Tool
	if json.Unmarshal(b, &disk) != nil {
		return false
	}
	return key(disk) == key(t)
}
