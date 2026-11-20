package toolenv

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"debug/elf"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/colony-2/c2j/pkg/ops/process"
)

const pnpmVersion = "12.10.1"
const pnpmWrapperIntegrity = "sha512-ukCjfrGjcNYP6oxM8dNkwTvMzvUY6YrSvO6VT1OdONo3RY5YJho/pV4GUjRQ6g//+fSXsTW+QfLBXBZCusrObA=="
const pnpmAMD64Integrity = "sha512-ZByV0DRgWzGaDSjT5ddRBASdwz+r/qPMYh7o6RGmPQjtRGNFbSl94xY2DcAM8mqCOZJUUDtAXQ+wpBXcdpKlkg=="

const pnpmARM64Integrity = "sha512-oBdMp69ScC9UnniG4qZAQVr2fBDSXjm4yk/72vkEVyrlwS23yMxcRQl7Qdo3sgXPAygY6TWnPRKm4rXzjoyW7w=="

type pnpmPlatform struct {
	Package    string
	CPU        string
	Machine    string
	ELFMachine elf.Machine
	Integrity  string
}

// Select the installed optional dependency and its independent archive pin from
// the Go process architecture, never from ambient pnpm platform overrides.
func pnpmNativePlatform(goos, goarch string) (pnpmPlatform, error) {
	if goos == "linux" {
		switch goarch {
		case "amd64":
			return pnpmPlatform{"@pnpm/exe.linux-x64", "x64", "x86_64", elf.EM_X86_64, pnpmAMD64Integrity}, nil
		case "arm64":
			return pnpmPlatform{"@pnpm/exe.linux-arm64", "arm64", "aarch64", elf.EM_AARCH64, pnpmARM64Integrity}, nil
		}
	}
	return pnpmPlatform{}, fmt.Errorf("pnpm 12.10.1 native preparation requires Linux amd64 or arm64/glibc; observed %s/%s", goos, goarch)
}

type pnpmNative struct {
	Version          string `json:"version"`
	Directory        string `json:"directory"`
	WrapperIntegrity string `json:"wrapper_integrity"`
	NativeIntegrity  string `json:"native_integrity"`
	PayloadSHA256    string `json:"payload_sha256"`
	PayloadSize      int64  `json:"payload_size"`
	ExecutedVersion  string `json:"executed_version"`
}

// Only cold setup downloads archives. Hash pinned archives before reading their
// members; never extract archive-controlled paths into the filesystem.
func pnpmArchive(ctx context.Context, name, integrity string) (map[string][]byte, error) {
	url := "https://registry.npmjs.org/" + name + "/-/" + name[strings.LastIndex(name, "/")+1:] + "-" + pnpmVersion + ".tgz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch verified pnpm %s archive failed", name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pnpm %s archive returned HTTP %d", name, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 128<<20 {
		return nil, fmt.Errorf("pnpm archive too large")
	}
	return readPnpmArchive(raw, integrity)
}
func readPnpmArchive(raw []byte, integrity string) (map[string][]byte, error) {
	sum := sha512.Sum512(raw)
	if "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != integrity {
		return nil, fmt.Errorf("pnpm archive integrity mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, 256<<20))
	files := map[string][]byte{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		switch h.Name {
		case "package/package.json", "package/pnpm":
			if h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > 128<<20 || files[h.Name] != nil {
				return nil, fmt.Errorf("invalid pnpm archive member %s", h.Name)
			}
			files[h.Name], err = io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
		}
	}
	if files["package/package.json"] == nil {
		return nil, fmt.Errorf("pnpm archive has no manifest")
	}
	return files, nil
}
func nativeELF(path string, platform pnpmPlatform) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("pnpm payload is not a native ELF: %w", err)
	}
	defer f.Close()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != platform.ELFMachine || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return fmt.Errorf("pnpm payload is not a %s executable", platform.CPU)
	}
	return nil
}
func validatePnpmNative(ctx context.Context, dir string, t *Tool) error {
	platform, err := pnpmNativePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	wrapper, err := pnpmArchive(ctx, "pnpm", pnpmWrapperIntegrity)
	if err != nil {
		return err
	}
	native, err := pnpmArchive(ctx, platform.Package, platform.Integrity)
	if err != nil {
		return err
	}
	return validateInstalledPnpm(ctx, dir, t, wrapper, native, platform)
}
func validateInstalledPnpm(ctx context.Context, dir string, t *Tool, wrapper, native map[string][]byte, platform pnpmPlatform) error {
	wrapperDir := filepath.Join(dir, "node_modules", "pnpm")
	actual, err := os.ReadFile(filepath.Join(wrapperDir, "package.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, wrapper["package/package.json"]) {
		return fmt.Errorf("installed pnpm wrapper differs from verified archive")
	}
	var manifest struct {
		Name     string            `json:"name"`
		Version  string            `json:"version"`
		Bin      map[string]string `json:"bin"`
		Optional map[string]string `json:"optionalDependencies"`
	}
	if json.Unmarshal(actual, &manifest) != nil || manifest.Name != "pnpm" || manifest.Version != pnpmVersion || manifest.Optional[platform.Package] != pnpmVersion {
		return fmt.Errorf("wrong pnpm wrapper/native version")
	}
	// Dependencies live adjacent to pnpm in the isolated virtual-store entry.
	resolved, err := filepath.EvalSymlinks(wrapperDir)
	if err != nil {
		return err
	}
	nativeDir := filepath.Join(filepath.Dir(resolved), filepath.FromSlash(platform.Package))
	actual, err = os.ReadFile(filepath.Join(nativeDir, "package.json"))
	if err != nil {
		return fmt.Errorf("required pnpm native package missing: %w", err)
	}
	if !bytes.Equal(actual, native["package/package.json"]) {
		return fmt.Errorf("installed pnpm native metadata differs from verified archive")
	}
	var meta struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		OS      []string `json:"os"`
		CPU     []string `json:"cpu"`
		Libc    []string `json:"libc"`
	}
	if json.Unmarshal(actual, &meta) != nil || meta.Name != platform.Package || meta.Version != pnpmVersion || !reflect.DeepEqual(meta.OS, []string{"linux"}) || !reflect.DeepEqual(meta.CPU, []string{platform.CPU}) || !reflect.DeepEqual(meta.Libc, []string{"glibc"}) {
		return fmt.Errorf("wrong pnpm native platform metadata")
	}
	payload := native["package/pnpm"]
	if len(payload) == 0 {
		return fmt.Errorf("verified native archive has no payload")
	}
	hash := sha256.Sum256(payload)
	digest := hex.EncodeToString(hash[:])
	for _, path := range []string{filepath.Join(nativeDir, "pnpm"), filepath.Join(wrapperDir, "pnpm"), filepath.Join(wrapperDir, "pn"), filepath.Join(wrapperDir, "pnpx"), filepath.Join(wrapperDir, "pnx")} {
		e, err := recordFile(path)
		if err != nil {
			return err
		}
		if e.SHA256 != digest || e.Size != int64(len(payload)) || e.TargetMode&0111 == 0 {
			return fmt.Errorf("incomplete or altered pnpm native executable %s", filepath.Base(path))
		}
		if err = nativeELF(path, platform); err != nil {
			return err
		}
	}
	for _, name := range []string{"pnpm", "pn", "pnpx", "pnx"} {
		if manifest.Bin[name] != name {
			return fmt.Errorf("unexpected pnpm alias metadata %s", name)
		}
		path := filepath.Join(t.Bin, name)
		stat, err := os.Lstat(path)
		if err != nil {
			return err
		}
		target := filepath.Join(wrapperDir, name)
		if stat.Mode()&os.ModeSymlink != 0 {
			a, e := filepath.EvalSymlinks(path)
			if e != nil {
				return e
			}
			b, e := filepath.EvalSymlinks(target)
			if e != nil || a != b {
				return fmt.Errorf("pnpm alias selects unexpected target %s", name)
			}
		} else {
			body, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			// pnpm's Unix shim must name the verified wrapper target. Execution below
			// additionally verifies the ordinary entrypoint before evidence publication.
			if stat.Mode()&0111 == 0 || !pnpmLauncherNamesTarget(body, t.Bin, target) {
				return fmt.Errorf("unrecognized pnpm launcher %s", name)
			}
		}
	}
	out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{WorkingDir: dir, Command: []string{filepath.Join(t.Bin, "pnpm"), "--version"}})
	if err != nil {
		return fmt.Errorf("pnpm native execution failed: %w: %s", err, pnpmRedact(string(stderr), pnpmSecrets(nil)))
	}
	if strings.TrimSpace(string(out)) != pnpmVersion {
		return fmt.Errorf("pnpm native execution returned wrong version")
	}
	t.Pnpm.Native = &pnpmNative{Version: pnpmVersion, Directory: nativeDir, WrapperIntegrity: pnpmWrapperIntegrity, NativeIntegrity: platform.Integrity, PayloadSHA256: digest, PayloadSize: int64(len(payload)), ExecutedVersion: pnpmVersion}
	return nil
}

// pnpm can link through node_modules/pnpm or directly into its virtual store.
// New cmd-shim versions also quote the basedir and relative path separately.
// Recognize an exec of the verified target, not a comment naming that target.
func pnpmLauncherNamesTarget(body []byte, bin, target string) bool {
	if !bytes.HasPrefix(body, []byte("#!/bin/sh\n")) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	for _, path := range []string{target, resolved} {
		rel, err := filepath.Rel(bin, path)
		if err != nil || strings.ContainsAny(rel, "'\"$`\n\r") {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, variable := range []string{"basedir", "basedir_abs"} {
			for _, prefix := range []string{"exec \"$" + variable + "/" + rel + "\"", "exec \"$" + variable + "\"/'" + rel + "'"} {
				for _, line := range strings.Split(string(body), "\n") {
					line = strings.TrimSpace(line)
					rest, ok := strings.CutPrefix(line, prefix)
					if ok && len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') && strings.TrimSpace(rest) == `"$@"` {
						return true
					}
				}
			}
		}
	}
	return false
}

func nativePnpmEvidenceReady(t Tool) bool {
	return nativePnpmEvidenceReadyOn(t, runtime.GOOS, runtime.GOARCH)
}

func nativePnpmEvidenceReadyOn(t Tool, goos, goarch string) bool {
	platform, err := pnpmNativePlatform(goos, goarch)
	if err != nil || t.Pnpm == nil {
		return false
	}
	n := t.Pnpm.Native
	d := t.Pnpm.Diagnostic
	if n == nil || d.Platform != goos+"/"+goarch || d.Machine != platform.Machine || !strings.HasPrefix(d.Libc, "glibc ") || n.Version != pnpmVersion || n.ExecutedVersion != pnpmVersion || n.WrapperIntegrity != pnpmWrapperIntegrity || n.NativeIntegrity != platform.Integrity || n.PayloadSHA256 == "" || n.PayloadSize <= 0 {
		return false
	}
	for _, name := range []string{"pnpm", "pn", "pnpx", "pnx"} {
		found := false
		for _, exe := range t.Executables {
			if exe == name {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
