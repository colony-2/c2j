package toolenv

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/stretchr/testify/require"
)

func pythonTestTools(t *testing.T) (string, string) {
	t.Helper()
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv is required for real Python preparation")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 is required")
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "uv.log")
	wrapper := "#!/bin/sh\n" + shellcmd.LiteralLine("uv") + " >> " + shellcmd.QuotePOSIX(log) + "\nexec " + shellcmd.QuotePOSIX(uv) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(wrapper), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	t.Setenv("UV_PYTHON_DOWNLOADS", "never")
	t.Setenv("UV_NO_INDEX", "true")
	return filepath.Join(bin, "uv"), log
}

// Real uv installs locally served wheels, so this test needs no registry or network access beyond loopback.
func pythonWheel(t *testing.T, version string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	prefix := "c2j_probe-" + version + ".dist-info/"
	files := map[string]string{
		"c2j_probe.py":              "VERSION = '" + version + "'\ndef main():\n    print(VERSION)\n",
		prefix + "entry_points.txt": "[console_scripts]\nc2j-probe = c2j_probe:main\n",
		prefix + "METADATA":         "Metadata-Version: 2.1\nName: c2j-probe\nVersion: " + version + "\nProvides-Extra: images\n",
		prefix + "WHEEL":            "Wheel-Version: 1.0\nGenerator: c2j-test\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
	}
	var record strings.Builder
	for name, body := range files {
		writer, err := w.Create(name)
		require.NoError(t, err)
		_, err = writer.Write([]byte(body))
		require.NoError(t, err)
		fmt.Fprintf(&record, "%s,,\n", name)
	}
	record.WriteString(prefix + "RECORD,,\n")
	writer, err := w.Create(prefix + "RECORD")
	require.NoError(t, err)
	_, err = writer.Write([]byte(record.String()))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return b.Bytes()
}

func TestPythonPreparationReuseIsolationAndRecovery(t *testing.T) {
	wrapper, log := pythonTestTools(t)
	v1, v2 := pythonWheel(t, "1.0"), pythonWheel(t, "2.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "2.0") {
			_, _ = w.Write(v2)
		} else {
			_, _ = w.Write(v1)
		}
	}))
	defer server.Close()
	request := func(v string) *execution.Python {
		return &execution.Python{RequiresPython: ">=3.9", Dependencies: []string{"c2j-probe[images] @ " + server.URL + "/c2j_probe-" + v + "-py3-none-any.whl", `absent-package; python_version < "2"`}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := Manager{Root: t.TempDir()}
	seedRunnerTool(t, &m, "nix:fixture#python", "wrong-python", "python", "python3")
	scopes := []Scope{{ID: "outer", Python: &execution.Python{Dependencies: []string{"invalid !!!"}}}, {ID: "cell-a", Python: request("1.0"), Packages: []string{"nix:fixture#python"}}}
	var environments [3]Environment
	var errors [3]error
	var diagnostics [3]Diagnostics
	var wg sync.WaitGroup
	for i := range environments {
		wg.Add(1)
		go func() { defer wg.Done(); environments[i], diagnostics[i], errors[i] = m.Prepare(ctx, scopes) }()
	}
	wg.Wait()
	prepared := 0
	for i, e := range environments {
		require.NoError(t, errors[i])
		require.True(t, e.Ready())
		if diagnostics[i].Python.Outcome == "prepared" {
			prepared++
		}
		require.Equal(t, environments[0].Python.Path, e.Python.Path)
	}
	require.Equal(t, 1, prepared)
	env := environments[0]
	probe := func(e Environment, want string) {
		out, stderr, err := process.ExecuteProcess(e.Bind(ctx), process.RunRequest{Command: []string{"python3", "-c", `import c2j_probe, subprocess, sys; print(c2j_probe.VERSION); subprocess.check_call(['python', '-c', 'import c2j_probe; print(c2j_probe.VERSION)'])`}, Env: map[string]string{"PYTHONPATH": "/invalid", "PYTHONHOME": "/invalid", "VIRTUAL_ENV": "/invalid", "PYTHONNOUSERSITE": "0"}})
		require.NoError(t, err, string(stderr))
		require.Equal(t, want+"\n"+want+"\n", string(out))
	}
	probe(env, "1.0")
	other, _, err := m.Prepare(ctx, []Scope{{ID: "cell-b", Python: request("2.0"), Packages: []string{"uv:c2j-probe @ " + server.URL + "/c2j_probe-1.0-py3-none-any.whl"}}})
	require.NoError(t, err)
	probe(other, "2.0")
	cliOut, cliErr, err := process.ExecuteProcess(other.Bind(ctx), process.RunRequest{Command: []string{"c2j-probe"}})
	require.NoError(t, err, string(cliErr))
	require.Equal(t, "1.0\n", string(cliOut))
	probe(env, "1.0")
	require.NotEqual(t, env.Python.Path, other.Python.Path)
	before, err := os.ReadFile(log)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 91\n"), 0700))
	t.Setenv("UV_OFFLINE", "true")
	require.NoError(t, os.RemoveAll(os.Getenv("UV_CACHE_DIR")))
	again, diag, err := m.Prepare(ctx, []Scope{{ID: "different-cell", Python: request("1.0")}})
	require.NoError(t, err)
	require.Equal(t, "reused", diag.Python.Outcome)
	require.Equal(t, env.Python, again.Python)
	probe(again, "1.0")
	after, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, before, after)
	// Lost installed files invalidate readiness; failure cannot publish a replacement.
	for _, f := range env.Python.Files {
		if strings.HasSuffix(f.Path, "c2j_probe.py") {
			require.NoError(t, os.Remove(f.Path))
			break
		}
	}
	require.False(t, env.Ready())
	_, diag, err = m.Prepare(ctx, scopes)
	require.Error(t, err)
	require.Equal(t, "failed", diag.Python.Outcome)
	require.NotEmpty(t, diag.Python.CommandLogs)
	require.True(t, other.Ready())
}

func TestPythonPreparationErrorsAndEmptyEnvironment(t *testing.T) {
	pythonTestTools(t)
	m := Manager{Root: t.TempDir()}
	for _, request := range []*execution.Python{
		{RequiresPython: ">=99", Dependencies: []string{}},
		{RequiresPython: "invalid !!!", Dependencies: []string{}},
		{Dependencies: []string{"not a valid requirement !!!"}},
		{Dependencies: []string{"no-such-package-c2j==0"}},
	} {
		_, diag, err := m.Prepare(context.Background(), []Scope{{ID: "bad", Python: request}})
		require.Error(t, err)
		require.Equal(t, "failed", diag.Python.Outcome)
		require.NotEmpty(t, diag.Python.CommandLogs)
	}
	env, diag, err := m.Prepare(context.Background(), []Scope{{ID: "empty", Python: &execution.Python{Dependencies: []string{}}}})
	require.NoError(t, err)
	require.Equal(t, "prepared", diag.Python.Outcome)
	require.True(t, env.Ready())
	require.JSONEq(t, "[]", string(env.Python.Resolved))
}

func TestPythonSourceBuildAndLinkedReuse(t *testing.T) {
	pythonTestTools(t)
	buildLog := filepath.Join(t.TempDir(), "builds")
	t.Setenv("C2J_PYTHON_BUILD_LOG", buildLog)
	wheel := pythonWheel(t, "1.0")
	backend := `import base64, os
from pathlib import Path
def build_wheel(wheel_directory, config_settings=None, metadata_directory=None):
    with open(os.environ['C2J_PYTHON_BUILD_LOG'], 'a') as log:
        log.write('build\n')
    name = 'c2j_probe-1.0-py3-none-any.whl'
    Path(wheel_directory, name).write_bytes(base64.b64decode('` + base64.StdEncoding.EncodeToString(wheel) + `'))
    return name
`
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tarball := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"pyproject.toml": "[build-system]\nrequires = []\nbuild-backend = 'backend'\nbackend-path = ['.']\n[project]\nname = 'c2j-probe'\nversion = '1.0'\n",
		"backend.py":     backend,
	} {
		require.NoError(t, tarball.WriteHeader(&tar.Header{Name: "c2j_probe-1.0/" + name, Mode: 0600, Size: int64(len(body))}))
		_, err := tarball.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tarball.Close())
	require.NoError(t, gz.Close())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()
	request := &execution.Python{Dependencies: []string{"c2j-probe @ " + server.URL + "/c2j_probe-1.0.tar.gz"}}
	m := Manager{Root: t.TempDir()}
	first, _, err := m.Prepare(context.Background(), []Scope{{ID: "first", Python: request}})
	require.NoError(t, err)
	before, err := os.ReadFile(buildLog)
	require.NoError(t, err)
	require.Equal(t, "build\n", string(before))
	// A different complete environment can reuse the previously built wheel.
	extra := execution.ChoosePython(nil, request)
	extra.Dependencies = append(extra.Dependencies, `absent; python_version < "2"`)
	second, _, err := m.Prepare(context.Background(), []Scope{{ID: "second", Python: extra}})
	require.NoError(t, err)
	after, err := os.ReadFile(buildLog)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NotEqual(t, first.Python.Path, second.Python.Path)
	packageFile := func(e Environment) string {
		for _, f := range e.Python.Files {
			if strings.HasSuffix(f.Path, "c2j_probe.py") {
				return f.Path
			}
		}
		t.Fatal("missing package file")
		return ""
	}
	a, err := os.Stat(packageFile(first))
	require.NoError(t, err)
	b, err := os.Stat(packageFile(second))
	require.NoError(t, err)
	if runtime.GOOS == "linux" {
		require.True(t, os.SameFile(a, b), "uv should hardlink cached package files on this shared filesystem")
	}
	// A different native compiler/library environment must rebuild its wheel.
	third, _, err := m.preparePython(context.Background(), []Scope{{ID: "third", Python: request}}, &nativeEnvironment{Identity: "different-native-tools"})
	require.NoError(t, err)
	require.NotEqual(t, first.Python.Path, third.Path)
	rebuilt, err := os.ReadFile(buildLog)
	require.NoError(t, err)
	require.Equal(t, "build\nbuild\n", string(rebuilt))
	require.NoError(t, os.RemoveAll(os.Getenv("UV_CACHE_DIR")))
	for _, e := range []Environment{first, second} {
		out, stderr, err := process.ExecuteProcess(e.Bind(context.Background()), process.RunRequest{Command: []string{"python", "-c", "import c2j_probe; print(c2j_probe.VERSION)"}})
		require.NoError(t, err, string(stderr))
		require.Equal(t, "1.0\n", string(out))
	}
}

func TestPythonInterruptedPreparationDoesNotPublish(t *testing.T) {
	wrapper, _ := pythonTestTools(t)
	original, err := os.ReadFile(wrapper)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nsleep 10\n"), 0700))
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "empty", Python: &execution.Python{Dependencies: []string{}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, diag, err := m.Prepare(ctx, scopes)
	require.Error(t, err)
	require.Equal(t, "failed", diag.Python.Outcome)
	matches, err := filepath.Glob(filepath.Join(m.Root, "python", "*", "ready.json"))
	require.NoError(t, err)
	require.Empty(t, matches)
	require.NoError(t, os.WriteFile(wrapper, original, 0700))
	env, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Equal(t, "prepared", diag.Python.Outcome)
}

func TestRealPythonPillow(t *testing.T) {
	if os.Getenv("C2J_TEST_REAL_PYTHON") != "1" {
		t.Skip("set C2J_TEST_REAL_PYTHON=1 for the registry-backed Pillow smoke test")
	}
	pythonTestTools(t)
	t.Setenv("UV_NO_INDEX", "false")
	m := Manager{Root: t.TempDir()}
	env, _, err := m.Prepare(context.Background(), []Scope{{ID: "pillow", Python: &execution.Python{RequiresPython: ">=3.10", Dependencies: []string{"Pillow==12.0.0"}}}})
	require.NoError(t, err)
	out, stderr, err := process.ExecuteProcess(env.Bind(context.Background()), process.RunRequest{Command: []string{"python3", "-c", `from io import BytesIO
from PIL import Image
import sys
b = BytesIO()
Image.new('RGB', (2, 2)).save(b, format='PNG')
b.seek(0)
with Image.open(b) as image:
    image.load()
    assert image.size == (2, 2)
print(sys.version)
print('Pillow PNG round-trip passed')
`}})
	require.NoError(t, err, string(stderr))
	t.Log(string(out))
	require.Contains(t, string(out), "Pillow PNG round-trip passed")
}
