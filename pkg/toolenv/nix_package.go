package toolenv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops/process"
)

// NixPackage is evaluation metadata, not an installed package. StorePath pins
// realization to exactly the output described by Manifest, even if a ref moves.
type NixPackage struct {
	StorePath string          `json:"store_path"`
	System    string          `json:"system"`
	Manifest  json.RawMessage `json:"manifest"`
}

var nixStorePath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[^/\s]+$`)

func NixSystem() (string, error) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") {
		return "", fmt.Errorf("unsupported Nix execution platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return arch + "-" + runtime.GOOS, nil
}

func (p NixPackage) Validate() error {
	if !nixStorePath.MatchString(p.StorePath) {
		return fmt.Errorf("invalid Nix package output path %q", p.StorePath)
	}
	system, err := NixSystem()
	if err != nil {
		return err
	}
	if p.System != system {
		return fmt.Errorf("Nix op targets %q but executor is %q", p.System, system)
	}
	return nil
}

// DescribeNixPackage evaluates passthru.c2j and the output identity together.
// Import-from-derivation is forbidden: metadata inspection cannot realize code.
func DescribeNixPackage(ctx context.Context, reference, baseDir string) (*NixPackage, error) {
	manager, spec, err := execution.ParsePackage(reference)
	if err != nil {
		return nil, err
	}
	if manager != "nix" {
		return nil, fmt.Errorf("expected a nix: package reference")
	}
	out, err := runNix(ctx, baseDir, "eval", "--json",
		"--option", "allow-import-from-derivation", "false",
		"--apply", "p: { manifest = p.c2j; store_path = p.outPath; system = p.system; }", "--", spec)
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", reference, err)
	}
	var p NixPackage
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("decode Nix op metadata: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// PrepareNixPackage realizes only the pinned output, with a retained GC root.
// It never re-evaluates the original selector or builds missing outputs.
func (m *Manager) PrepareNixPackage(ctx context.Context, p NixPackage) (root string, reused bool, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if err = p.Validate(); err != nil {
		return
	}
	if !filepath.IsAbs(m.Root) {
		return "", false, fmt.Errorf("tool cache root must be absolute")
	}
	dir := filepath.Join(m.Root, "nix-ops", key(p.StorePath))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return
	}
	unlock, err := lock(ctx, dir+".lock")
	if err != nil {
		return "", false, err
	}
	defer unlock()
	root = filepath.Join(dir, "result")
	if target, e := filepath.EvalSymlinks(root); e == nil && target == p.StorePath {
		return root, true, nil
	}
	_, err = runNix(ctx, dir, "build", "--out-link", root, "--", p.StorePath)
	if err != nil {
		return "", false, fmt.Errorf("prepare Nix op %s: %w", p.StorePath, err)
	}
	target, err := filepath.EvalSymlinks(root)
	if err != nil || target != p.StorePath {
		return "", false, fmt.Errorf("Nix op did not produce pinned output %s", p.StorePath)
	}
	return root, false, nil
}

func runNix(ctx context.Context, dir string, args ...string) ([]byte, error) {
	out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{WorkingDir: dir, Command: nixSetupCommand(args...)})
	if err != nil {
		detail := strings.TrimSpace(string(stderr))
		if len(detail) > 4096 {
			detail = detail[len(detail)-4096:]
		}
		return nil, fmt.Errorf("%w: %s", err, detail)
	}
	return out, nil
}
