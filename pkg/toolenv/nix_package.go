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
	if err := prefetchNixSource(ctx, baseDir, spec); err != nil {
		return nil, fmt.Errorf("prefetch %s: %w", reference, err)
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
	out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{
		WorkingDir: dir, Command: nixSetupCommand(args...), Env: map[string]string{"LC_ALL": "C"},
		StopTreeOnFailure: process.CheckedTreeCleanupSupported,
	})
	if err != nil {
		return nil, nixProcessError(err, string(stderr))
	}
	return out, nil
}

func nixProcessError(err error, detail string) error {
	detail = strings.TrimSpace(detail)
	if len(detail) > 4096 {
		detail = detail[len(detail)-4096:]
	}
	return fmt.Errorf("%w: %s", err, detail)
}

var pinnedGitHubSource = regexp.MustCompile(`^github:[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+/[0-9a-f]{40}$`)

// prefetchNixSource is a worker-owned store write that cannot evaluate the
// flake or run a builder. Error records from evaluation (including throw) are
// intentionally never considered evidence, even if they match verbatim.
func prefetchNixSource(ctx context.Context, dir, spec string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, _, _ := strings.Cut(spec, "#")
	if !pinnedGitHubSource.MatchString(source) || os.Getenv("NIX_REMOTE") != "local" || !process.CheckedTreeCleanupSupported {
		return nil
	}
	verified := defaultLocalNixLayout(ctx, dir)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !verified {
		return nil
	}
	_, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{
		WorkingDir: dir, Env: map[string]string{"LC_ALL": "C"}, StopTreeOnAnyFailure: true,
		Command: nixSetupCommand("--log-format", "internal-json", "flake", "prefetch", "--json", "--", source),
	})
	return nixPrefetchResult(ctx, stderr, err)
}

func defaultLocalNixLayout(ctx context.Context, dir string) bool {
	store, err := runNix(ctx, dir, "config", "show", "store")
	uri := strings.TrimSpace(string(store))
	if err != nil || ctx.Err() != nil || uri != "local" && uri != "local://" {
		return false
	}
	layout, _, err := process.ExecuteProcess(ctx, process.RunRequest{
		WorkingDir: dir, Command: []string{"nix", "__dump-cli"}, Env: map[string]string{"LC_ALL": "C"},
		StopTreeOnAnyFailure: true,
	})
	if err != nil {
		return false
	}
	var cli struct {
		Stores map[string]struct {
			Settings map[string]struct{ Value json.RawMessage }
		}
	}
	if json.Unmarshal(layout, &cli) != nil {
		return false
	}
	settings := cli.Stores["Local Store"].Settings
	return string(settings["root"].Value) == "null" &&
		string(settings["store"].Value) == `"/nix/store"` &&
		string(settings["real"].Value) == `"/nix/store"` &&
		string(settings["state"].Value) == `"/nix/var/nix"` &&
		string(settings["log"].Value) == `"/nix/var/log/nix"`
}

func nixPrefetchResult(ctx context.Context, stderr []byte, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err == nil {
		return nil
	}
	if path, message, confirmed := nixPrefetchDiskFailure(err, stderr); confirmed {
		return &nixDiskError{error: nixProcessError(err, message), path: path}
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() > 0 {
		return nil
	}
	return nixProcessError(err, string(stderr))
}
