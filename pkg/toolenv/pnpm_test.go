package toolenv

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pnpmFixture(t *testing.T) (string, string) {
	t.Helper()
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	t.Setenv("PNPM_FIXTURE_LOG", log)
	body := `#!/bin/sh
set -eu
case "$1" in
 --version) echo "${PNPM_FIXTURE_VERSION:-12.21.0}"; exit 0;;
 config)
  if [ -n "${PNPM_FIXTURE_CONFIG:-}" ]; then printf '%s\n' "$PNPM_FIXTURE_CONFIG"
  elif [ -f pnpm-workspace.yaml ]; then echo '{"dangerouslyAllowAllBuilds":true,"strictDepBuilds":true,"ignoreScripts":false,"optional":true}'
  else echo '{}'; fi
  exit 0;;
esac
[ "$CI" = true ]
[ "$1 $2 $3 $4" = 'add --workspace-root --save-exact --' ]
[ "$(cat pnpm-workspace.yaml)" = 'dangerouslyAllowAllBuilds: true
strictDepBuilds: true
ignoreScripts: false
optional: true' ]
printf '%s\n' "$5" >> "$PNPM_FIXTURE_LOG"
if [ -n "${PNPM_FIXTURE_WAIT:-}" ]; then : > "$PNPM_FIXTURE_WAIT"; sleep 30; fi
if [ "${PNPM_FIXTURE_FAIL:-}" = yes ]; then echo 'required build failed secret-fixture-token' >&2; exit 9; fi
mkdir -p node_modules/.bin
printf '#!/bin/sh\necho retained\n' > node_modules/.bin/tool
chmod 700 node_modules/.bin/tool
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "pnpm"), []byte(body), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin, log
}
func pnpmScope() []Scope { return []Scope{{ID: "op", Packages: []string{"pnpm:fixture@1"}}} }

func TestPnpmPrivatePolicyAndWarmReadiness(t *testing.T) {
	bootstrap, log := pnpmFixture(t)
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "op", Packages: []string{"pnpm:fixture@1", "pnpm:unrelated@2"}}}
	env, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	for _, d := range diag.Tools {
		require.Equal(t, "validated", d.Pnpm.Validation)
		require.Equal(t, "12.21.0", d.Pnpm.Version)
		require.Equal(t, pnpmPolicy, d.Pnpm.Policy)
		require.NotEmpty(t, d.Pnpm.NodeVersion)
	}
	for _, tool := range env.Tools {
		require.NotEqual(t, key([]string{"v1", runtime.GOOS, runtime.GOARCH, tool.Reference}), tool.Identity)
		require.Equal(t, packageIdentity("pnpm", tool.Reference), tool.Identity)
	}
	require.Equal(t, key([]string{"v1", runtime.GOOS, runtime.GOARCH, "uv:fixture"}), packageIdentity("uv", "uv:fixture"))
	require.Equal(t, key([]string{"nix-v2", runtime.GOOS, runtime.GOARCH, "nix:fixture#x"}), packageIdentity("nix", "nix:fixture#x"))
	before, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "fixture@1\nunrelated@2\n", string(before))
	// A healthy warm hit neither discovers nor invokes a bootstrap. Retaining
	// binding evidence also avoids rewriting fallback runners when PATH changes.
	require.NoError(t, os.Remove(filepath.Join(bootstrap, "pnpm")))
	t.Setenv("PATH", t.TempDir())
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	var restored Environment
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.True(t, restored.Ready())
	again, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.Equal(t, env, again)
	for _, d := range diag.Tools {
		require.Equal(t, "reused", d.Outcome)
	}
	after, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestPnpmRejectsConflictingOrUnsupportedBootstrap(t *testing.T) {
	for _, tc := range []struct{ name, version, config, want string }{
		{"old", "9.15.0", "", "unsupported pnpm bootstrap"},
		{"unparseable", "unknown", "", "unsupported pnpm bootstrap"},
		{"scripts", "", `{"ignore-scripts":true}`, "ignorescripts"},
		{"optionals", "", `{"optional":false}`, "optional"},
		{"strict", "", `{"strictDepBuilds":false}`, "strictdepbuilds"},
		{"denial", "", `{"allowBuilds":{"fixture":false}}`, "allowbuilds"},
		{"permission", "", `{"permissions":{"fixture":{"build":false}}}`, "permissions"},
		{"legacy", "", `{"neverBuiltDependencies":["fixture"]}`, "neverbuiltdependencies"},
		{"cross", "", `{"supportedArchitectures":{"cpu":["arm64"]}}`, "supportedarchitectures"},
		{"ignored-policy", "", `{}`, "did not expose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, log := pnpmFixture(t)
			t.Setenv("PNPM_FIXTURE_VERSION", tc.version)
			t.Setenv("PNPM_FIXTURE_CONFIG", tc.config)
			m := Manager{Root: t.TempDir()}
			_, diag, err := m.Prepare(context.Background(), pnpmScope())
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, "failed", diag.Tools[0].Outcome)
			require.NotNil(t, diag.Tools[0].Pnpm)
			files, err := filepath.Glob(filepath.Join(m.Root, "packages", "*", "ready.json"))
			require.NoError(t, err)
			require.Empty(t, files)
			_, err = os.Stat(log)
			require.True(t, os.IsNotExist(err), "rejected policy must fail before add")
		})
	}
}

func TestPnpmInstallFailureWithholdsReadinessAndRedacts(t *testing.T) {
	pnpmFixture(t)
	t.Setenv("PNPM_FIXTURE_FAIL", "yes")
	t.Setenv("REGISTRY_AUTH_TOKEN", "secret-fixture-token")
	m := Manager{Root: t.TempDir()}
	_, diag, err := m.Prepare(context.Background(), pnpmScope())
	require.ErrorContains(t, err, "required build failed")
	require.NotContains(t, diag.Error, "secret-fixture-token")
	files, e := filepath.Glob(filepath.Join(m.Root, "packages", "*", "ready.json"))
	require.NoError(t, e)
	require.Empty(t, files)
	t.Setenv("PNPM_FIXTURE_FAIL", "")
	env, _, err := m.Prepare(context.Background(), pnpmScope())
	require.NoError(t, err)
	require.True(t, env.Ready())
}

func TestPnpmCacheDamageAndOldSerialization(t *testing.T) {
	for _, kind := range []string{"payload", "mode", "policy", "metadata", "retarget", "ready-record", "old-tool", "empty-exports", "old-binding", "binding-bytes", "binding-mode", "binding-record"} {
		t.Run(kind, func(t *testing.T) {
			pnpmFixture(t)
			m := Manager{Root: t.TempDir()}
			env, _, err := m.Prepare(context.Background(), pnpmScope())
			require.NoError(t, err)
			require.True(t, env.Ready())
			tool := &env.Tools[0]
			dir := filepath.Dir(filepath.Dir(tool.Bin))
			target := filepath.Join(tool.Bin, "tool")
			switch kind {
			case "payload":
				b, e := os.ReadFile(target)
				require.NoError(t, e)
				b[len(b)-2] = 'X'
				require.NoError(t, os.WriteFile(target, b, 0700))
			case "mode":
				require.NoError(t, os.Chmod(target, 0600))
			case "policy":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "pnpm-workspace.yaml"), []byte("ignoreScripts: true\n"), 0600))
			case "metadata":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"private":false}`), 0600))
			case "retarget":
				other := filepath.Join(dir, "other")
				b, e := os.ReadFile(target)
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(other, b, 0700))
				require.NoError(t, os.Remove(target))
				require.NoError(t, os.Symlink(other, target))
			case "ready-record":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "ready.json"), []byte(`{}`), 0600))
			case "old-tool":
				tool.Pnpm = nil
			case "empty-exports":
				tool.Executables = nil
			case "old-binding":
				env.Bindings = nil
			case "binding-bytes":
				require.NoError(t, os.WriteFile(filepath.Join(env.Path, "pnpm"), []byte("#!/bin/sh\necho ambient\n"), 0700))
			case "binding-mode":
				require.NoError(t, os.Chmod(filepath.Join(env.Path, "pnpm"), 0600))
			case "binding-record":
				require.NoError(t, os.Remove(filepath.Join(env.Path, "ready.json")))
			}
			require.False(t, env.Ready(), "damaged or old state must be unready")
			repaired, _, err := m.Prepare(context.Background(), pnpmScope())
			require.NoError(t, err)
			require.True(t, repaired.Ready())
		})
	}
}

func TestPnpmConcurrentPublication(t *testing.T) {
	_, log := pnpmFixture(t)
	m := Manager{Root: t.TempDir()}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env, _, err := m.Prepare(context.Background(), pnpmScope())
			if err == nil && !env.Ready() {
				err = os.ErrInvalid
			}
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	b, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "fixture@1\n", string(b))
}

func TestPnpmArchiveIntegrityAndELF(t *testing.T) {
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tr := tar.NewWriter(gz)
	data := []byte(`{"name":"pnpm","version":"12.10.1"}`)
	require.NoError(t, tr.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0644, Size: int64(len(data))}))
	_, err := tr.Write(data)
	require.NoError(t, err)
	require.NoError(t, tr.Close())
	require.NoError(t, gz.Close())
	hash := sha512.Sum512(raw.Bytes())
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(hash[:])
	files, err := readPnpmArchive(raw.Bytes(), integrity)
	require.NoError(t, err)
	require.Equal(t, data, files["package/package.json"])
	_, err = readPnpmArchive(append(raw.Bytes(), 0), integrity)
	require.ErrorContains(t, err, "integrity mismatch")
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			platform, err := pnpmNativePlatform("linux", arch)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "payload")
			require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho 12.10.1\n"), 0700))
			require.ErrorContains(t, nativeELF(path, platform), "not a native ELF")
			for _, machine := range []elf.Machine{elf.EM_X86_64, elf.EM_AARCH64} {
				header := pnpmELFHeader(machine)
				require.NoError(t, os.WriteFile(path, header, 0700))
				if machine == platform.ELFMachine {
					require.NoError(t, nativeELF(path, platform), "header check alone does not certify execution")
				} else {
					require.ErrorContains(t, nativeELF(path, platform), "not a "+platform.CPU+" executable")
				}
			}
		})
	}
}

func pnpmELFHeader(machine elf.Machine) []byte {
	header := make([]byte, 64)
	copy(header, "\x7fELF\x02\x01\x01")
	binary.LittleEndian.PutUint16(header[16:], 2)
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	return header
}

func TestPnpmNativePlatformAndEvidence(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			p, err := pnpmNativePlatform("linux", arch)
			require.NoError(t, err)
			if arch == "arm64" {
				require.Equal(t, pnpmPlatform{"@pnpm/exe.linux-arm64", "arm64", "aarch64", elf.EM_AARCH64, pnpmARM64Integrity}, p)
			} else {
				require.Equal(t, pnpmPlatform{"@pnpm/exe.linux-x64", "x64", "x86_64", elf.EM_X86_64, pnpmAMD64Integrity}, p)
			}
			require.NotEqual(t, pnpmAMD64Integrity, pnpmARM64Integrity)
			tool := Tool{Executables: []string{"pnpm", "pn", "pnpx", "pnx"}, Pnpm: &pnpmTool{
				Diagnostic: pnpmDiagnostic{Platform: "linux/" + arch, Machine: p.Machine, Libc: "glibc 2.43"},
				Native:     &pnpmNative{Version: pnpmVersion, WrapperIntegrity: pnpmWrapperIntegrity, NativeIntegrity: p.Integrity, PayloadSHA256: "verified-payload", PayloadSize: 64, ExecutedVersion: pnpmVersion},
			}}
			require.True(t, nativePnpmEvidenceReadyOn(tool, "linux", arch))
			other := "amd64"
			if arch == other {
				other = "arm64"
			}
			require.False(t, nativePnpmEvidenceReadyOn(tool, "linux", other), "retained evidence cannot cross architectures")
			otherPlatform, err := pnpmNativePlatform("linux", other)
			require.NoError(t, err)
			tool.Pnpm.Native.NativeIntegrity = otherPlatform.Integrity
			require.False(t, nativePnpmEvidenceReadyOn(tool, "linux", arch), "the selected architecture needs its own archive pin")
			tool.Pnpm.Native.NativeIntegrity = p.Integrity
			tool.Pnpm.Diagnostic.Machine = otherPlatform.Machine
			require.False(t, nativePnpmEvidenceReadyOn(tool, "linux", arch))
		})
	}
	for _, host := range [][2]string{{"darwin", "arm64"}, {"linux", "386"}, {"linux", "riscv64"}} {
		_, err := pnpmNativePlatform(host[0], host[1])
		require.ErrorContains(t, err, "requires Linux amd64 or arm64/glibc")
	}
}

func TestPnpmNativeHostMismatch(t *testing.T) {
	bin, log := pnpmFixture(t)
	_, platformErr := pnpmNativePlatform(runtime.GOOS, runtime.GOARCH)
	if platformErr == nil {
		// Even a supported Go host must reject an incompatible observed machine
		// before invoking add or retaining readiness.
		require.NoError(t, os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/sh\nprintf '%s\\n' incompatible-machine\n"), 0700))
	}
	m := Manager{Root: t.TempDir()}
	_, diag, err := m.Prepare(context.Background(), []Scope{{ID: "op", Packages: []string{"pnpm:pnpm@12.10.1"}}})
	if platformErr == nil {
		require.ErrorContains(t, err, "unsupported native host")
	} else {
		require.ErrorContains(t, err, "requires Linux amd64 or arm64/glibc")
	}
	require.Equal(t, "failed", diag.Tools[0].Pnpm.Validation)
	_, err = os.Stat(log)
	require.True(t, os.IsNotExist(err))
	require.False(t, pnpmReady(Tool{Reference: "pnpm:pnpm@12.10.1"}))
}

func TestPnpmLocalReadyDoesNotExecute(t *testing.T) {
	pnpmFixture(t)
	m := Manager{Root: t.TempDir()}
	env, _, err := m.Prepare(context.Background(), pnpmScope())
	require.NoError(t, err)
	// A script whose execution would leave evidence is perfectly valid retained
	// content for a generic tool. Ready must only read/hash it.
	target := filepath.Join(env.Tools[0].Bin, "tool")
	marker := filepath.Join(t.TempDir(), "executed")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0700))
	require.NoError(t, retainPnpm(&env.Tools[0]))
	// The manifest lives at the private project root, not node_modules.
	require.NoError(t, writeEvidence(filepath.Join(filepath.Dir(filepath.Dir(env.Tools[0].Bin)), "ready.json"), env.Tools[0]))
	env.Bindings, err = retainBindings(env)
	require.NoError(t, err)
	t.Setenv("PATH", t.TempDir())
	require.True(t, env.Ready())
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err))
	require.False(t, strings.Contains(pnpmRedact("https://u:p@host x-secret", []string{"x-secret"}), "x-secret"))
}

func TestPnpmCancellationAndLockRecovery(t *testing.T) {
	pnpmFixture(t)
	m := Manager{Root: t.TempDir()}
	marker := filepath.Join(t.TempDir(), "install-started")
	t.Setenv("PNPM_FIXTURE_WAIT", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := m.Prepare(ctx, pnpmScope()); done <- err }()
	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 3*time.Second, 10*time.Millisecond)
	// A second process/goroutine waiting for the same package lock must also
	// respect cancellation, without disturbing the current installer.
	waiting, stop := context.WithCancel(context.Background())
	defer stop()
	waitDone := make(chan error, 1)
	go func() { _, _, err := m.Prepare(waiting, pnpmScope()); waitDone <- err }()
	select {
	case err := <-waitDone:
		t.Fatalf("preparation returned while another installer holds the lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	stop()
	select {
	case err := <-waitDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("lock waiter did not respect cancellation")
	}
	cancel()
	require.Error(t, <-done)
	records, err := filepath.Glob(filepath.Join(m.Root, "packages", "*", "ready.json"))
	require.NoError(t, err)
	require.Empty(t, records)
	t.Setenv("PNPM_FIXTURE_WAIT", "")
	env, _, err := m.Prepare(context.Background(), pnpmScope())
	require.NoError(t, err)
	require.True(t, env.Ready())
}

func TestPnpmNativeLauncherTargets(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "node_modules", ".bin")
	store := filepath.Join(root, "node_modules", ".pnpm", "pnpm@12.10.1", "node_modules", "pnpm")
	require.NoError(t, os.MkdirAll(bin, 0700))
	require.NoError(t, os.MkdirAll(store, 0700))
	require.NoError(t, os.Symlink(store, filepath.Join(root, "node_modules", "pnpm")))
	for _, name := range []string{"pnpm", "pn", "pnpx", "pnx"} {
		require.NoError(t, os.WriteFile(filepath.Join(store, name), []byte("verified target"), 0700))
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(root, "node_modules", "pnpm", name)
			for _, exec := range []string{
				`exec "$basedir/../pnpm/` + name + `" "$@"`,
				`exec "$basedir/../.pnpm/pnpm@12.10.1/node_modules/pnpm/` + name + `" "$@"`,
				`exec "$basedir_abs"/'../.pnpm/pnpm@12.10.1/node_modules/pnpm/` + name + `'  "$@"`,
			} {
				require.True(t, pnpmLauncherNamesTarget([]byte("#!/bin/sh\n"+exec+"\n"), bin, target), exec)
			}
			for _, body := range []string{
				"#!/bin/sh\n# cmd-shim-target=../pnpm/" + name + "\nexec node fake.js\n",
				"#!/bin/sh\n# exec \"$basedir/../pnpm/" + name + "\" \"$@\"\n",
				"#!/bin/sh\nexec \"$basedir/../pnpm/" + name + "\"extra \"$@\"\n",
				"#!/bin/sh\nexec \"$basedir/../pnpm/" + name + "\"\"$@\"\n",
				"#!/bin/sh\nexec \"$basedir/../pnpm/wrong-alias\" \"$@\"\n",
				"#!/bin/sh\nexec \"$basedir/../pnpm/" + name + "\" --other-args\n",
			} {
				require.False(t, pnpmLauncherNamesTarget([]byte(body), bin, target), body)
			}
		})
	}
}

func TestPnpmNativePayloadRejections(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		platform, err := pnpmNativePlatform("linux", arch)
		require.NoError(t, err)
		for _, kind := range []string{"wrong-wrapper", "wrong-version", "missing-native", "wrong-libc", "wrong-cpu", "wrong-package", "wrong-elf", "placeholder", "wrong-optional", "alias-retarget", "execution"} {
			t.Run(arch+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				wrapperDir := filepath.Join(dir, "node_modules", "pnpm")
				nativeDir := filepath.Join(dir, "node_modules", filepath.FromSlash(platform.Package))
				bin := filepath.Join(dir, "node_modules", ".bin")
				require.NoError(t, os.MkdirAll(nativeDir, 0700))
				require.NoError(t, os.MkdirAll(wrapperDir, 0700))
				require.NoError(t, os.MkdirAll(bin, 0700))
				wrapper := []byte(fmt.Sprintf(`{"name":"pnpm","version":"12.10.1","bin":{"pnpm":"pnpm","pn":"pn","pnpx":"pnpx","pnx":"pnx"},"optionalDependencies":{%q:"12.10.1"}}`, platform.Package))
				meta := []byte(fmt.Sprintf(`{"name":%q,"version":"12.10.1","os":["linux"],"cpu":[%q],"libc":["glibc"]}`, platform.Package, platform.CPU))
				payload := pnpmELFHeader(platform.ELFMachine)
				if kind == "wrong-cpu" {
					meta = bytes.ReplaceAll(meta, []byte(`"cpu":["`+platform.CPU+`"]`), []byte(`"cpu":["unsupported"]`))
				}
				if kind == "wrong-package" {
					meta = bytes.ReplaceAll(meta, []byte(platform.Package), []byte("@pnpm/exe.linux-wrong"))
				}
				if kind == "wrong-elf" {
					otherMachine := elf.EM_AARCH64
					if platform.ELFMachine == otherMachine {
						otherMachine = elf.EM_X86_64
					}
					payload = pnpmELFHeader(otherMachine)
				}
				if kind == "wrong-version" {
					meta = bytes.ReplaceAll(meta, []byte("12.10.1"), []byte("12.10.2"))
				}
				if kind == "wrong-libc" {
					meta = bytes.ReplaceAll(meta, []byte("glibc"), []byte("musl"))
				}
				if kind == "wrong-optional" {
					wrapper = bytes.ReplaceAll(wrapper, []byte(`"`+platform.Package+`":"12.10.1"`), []byte(`"`+platform.Package+`":"12.10.2"`))
				}
				require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "package.json"), wrapper, 0600))
				require.NoError(t, os.WriteFile(filepath.Join(nativeDir, "package.json"), meta, 0600))
				require.NoError(t, os.WriteFile(filepath.Join(nativeDir, "pnpm"), payload, 0700))
				for _, name := range []string{"pnpm", "pn", "pnpx", "pnx"} {
					require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, name), payload, 0700))
					require.NoError(t, os.Symlink(filepath.Join(wrapperDir, name), filepath.Join(bin, name)))
				}
				switch kind {
				case "wrong-wrapper":
					require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "package.json"), []byte(`{}`), 0600))
				case "missing-native":
					require.NoError(t, os.Remove(filepath.Join(nativeDir, "package.json")))
				case "placeholder":
					require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "pnpm"), []byte("#!/bin/sh\necho 12.10.1\n"), 0700))
				case "alias-retarget":
					require.NoError(t, os.Remove(filepath.Join(bin, "pnpx")))
					require.NoError(t, os.Symlink(filepath.Join(wrapperDir, "pn"), filepath.Join(bin, "pnpx")))
				}
				tool := Tool{Reference: "pnpm:pnpm@12.10.1", Bin: bin, Pnpm: &pnpmTool{}}
				err := validateInstalledPnpm(context.Background(), dir, &tool, map[string][]byte{"package/package.json": wrapper}, map[string][]byte{"package/package.json": meta, "package/pnpm": payload}, platform)
				require.Error(t, err)
				require.Nil(t, tool.Pnpm.Native, "no native evidence may be published on failure")
				wants := map[string]string{"wrong-cpu": "wrong pnpm native platform", "wrong-package": "wrong pnpm native platform", "wrong-elf": "not a " + platform.CPU + " executable", "wrong-wrapper": "wrapper differs", "wrong-version": "wrong pnpm native platform", "missing-native": "native package missing", "wrong-libc": "wrong pnpm native platform", "placeholder": "incomplete or altered", "wrong-optional": "wrong pnpm wrapper/native", "alias-retarget": "unexpected target", "execution": "native execution failed"}
				require.ErrorContains(t, err, wants[kind])
			})
		}
	}
}
