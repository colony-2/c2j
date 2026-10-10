package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/toolenv"
	yaml "gopkg.in/yaml.v3"
)

const nixManifestPath = "share/c2j/op.json"

func describeNixOp(ctx context.Context, selector string, opts ResolveOptions) (*ResolvedOp, error) {
	p, err := toolenv.DescribeNixPackage(ctx, selector, opts.BaseDir)
	if err != nil {
		return nil, err
	}
	if err := validateExtensionOpManifest(p.Manifest); err != nil {
		return nil, err
	}
	var spec ExtensionOpSpec
	if err := yaml.Unmarshal(p.Manifest, &spec); err != nil {
		return nil, fmt.Errorf("decode passthru.c2j: %w", err)
	}
	r := &ResolvedOp{Selector: selector, Nix: p, Spec: spec}
	return RestoreResolvedOp(r)
}

func validateNixManifest(r *ResolvedOp) error {
	if r.Spec.InputSchema == nil || r.Spec.OutputSchema == nil {
		return fmt.Errorf("Nix op manifest requires input_schema and output_schema")
	}
	if r.Spec.Run != "" || r.Spec.Shell != "" || len(r.Spec.Command) == 0 {
		return fmt.Errorf("Nix op manifest requires command: [bin/<executable>, ...]; run and shell are not supported")
	}
	entry := r.Spec.Command[0]
	if !strings.HasPrefix(entry, "bin/") || filepath.ToSlash(filepath.Clean(entry)) != entry || strings.ContainsAny(entry, "\\\x00") {
		return fmt.Errorf("Nix op command must name an executable under bin/: %q", entry)
	}
	for _, ref := range r.Spec.Dependencies {
		manager, _, err := execution.ParsePackage(ref)
		if err != nil {
			return err
		}
		if manager == "nix" {
			return fmt.Errorf("Nix op dependencies must use uv: or pnpm:; declare Nix dependencies in the package definition")
		}
	}
	return nil
}

// PrepareNixOp downloads the output pinned during description, then checks that
// its installed manifest matches the one used for validation and defaults.
func PrepareNixOp(ctx context.Context, r *ResolvedOp) (*ResolvedOp, bool, error) {
	if r.Nix == nil {
		return nil, false, fmt.Errorf("missing Nix op description")
	}
	if err := validateNixManifest(r); err != nil {
		return nil, false, err
	}
	m, err := toolenv.Default()
	if err != nil {
		return nil, false, err
	}
	root, reused, err := m.PrepareNixPackage(ctx, *r.Nix)
	if err != nil {
		return nil, false, err
	}
	clone := *r
	clone.NixRoot = root
	clone.ProjectRoot = r.Nix.StorePath
	clone.OpDir = r.Nix.StorePath
	clone.SpecPath = filepath.Join(r.Nix.StorePath, nixManifestPath)
	b, err := os.ReadFile(clone.SpecPath)
	if err != nil {
		return nil, false, fmt.Errorf("read packaged op manifest: %w", err)
	}
	var described, installed any
	if json.Unmarshal(r.Nix.Manifest, &described) != nil || json.Unmarshal(b, &installed) != nil || !reflect.DeepEqual(described, installed) {
		return nil, false, fmt.Errorf("packaged %s differs from passthru.c2j", nixManifestPath)
	}
	if !clone.Ready() {
		return nil, false, fmt.Errorf("Nix op is missing its executable %q", r.Spec.Command[0])
	}
	return &clone, reused, nil
}

// Ready performs only local checks. Payload recovery belongs to the setup task.
func (r *ResolvedOp) Ready() bool {
	if _, err := os.Stat(r.SpecPath); err != nil {
		return false
	}
	if r.Nix != nil {
		target, err := filepath.EvalSymlinks(r.NixRoot)
		if err != nil || target != r.Nix.StorePath || len(r.Spec.Command) == 0 {
			return false
		}
		stat, err := os.Stat(filepath.Join(r.OpDir, r.Spec.Command[0]))
		if err != nil || stat.IsDir() || stat.Mode()&0111 == 0 {
			return false
		}
	}
	return true
}
