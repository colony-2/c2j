#!/usr/bin/env python3
"""Exercise native-library activation; no production c2j code is changed.

Requires Python 3, Docker, and network access. Uses the same Nix image/revision
as c2j's real-Nix integration test. No Python installation is needed in Docker.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


IMAGE = "nixos/nix:2.35.1"
REVISION = "b6018f87da91d19d0ab4cf979885689b469cdd41"
NIX = ["nix", "--extra-experimental-features", "nix-command flakes",
       "--max-jobs", "0", "--builders", "",
       "--option", "allow-import-from-derivation", "false"]

FLAKE = '''{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/REVISION";
  outputs = { self, nixpkgs }: let
    forAllSystems = nixpkgs.lib.genAttrs [ "aarch64-linux" "x86_64-linux" ];
  in { devShells = forAllSystems (system: let
    pkgs = nixpkgs.legacyPackages.${system};
    missing = preferLocalBuild: pkgs.runCommand
      "c2j-native-prototype-must-not-build-${if preferLocalBuild then "local" else "normal"}"
      { inherit preferLocalBuild; allowSubstitutes = false; }
      "echo SOURCE_BUILD_ATTEMPTED >&2; exit 99";
    shell = extra: pkgs.mkShell {
      nativeBuildInputs = [ pkgs.pkg-config ];
      buildInputs = [ pkgs.openssl pkgs.zlib ] ++ extra;
    };
  in {
    default = shell [];
    missing = shell [ (missing false) ];
    missingLocal = shell [ (missing true) ];
  }); };
}'''.replace("REVISION", REVISION)

PROBE = r'''#include <openssl/evp.h>
#include <openssl/crypto.h>
#include <zlib.h>
#include <stdio.h>
#include <string.h>

int main(void) {
    const unsigned char message[] = "c2j native libraries";
    unsigned char packed[128], unpacked[128], digest[EVP_MAX_MD_SIZE];
    uLongf packed_len = sizeof(packed), unpacked_len = sizeof(unpacked);
    unsigned int digest_len = 0;
    if (compress(packed, &packed_len, message, sizeof(message)) != Z_OK) return 1;
    if (uncompress(unpacked, &unpacked_len, packed, packed_len) != Z_OK) return 2;
    if (unpacked_len != sizeof(message) || memcmp(message, unpacked, sizeof(message))) return 3;
    if (!EVP_Digest(message, sizeof(message), digest, &digest_len, EVP_sha256(), NULL)) return 4;
    if (digest_len != 32) return 5;
    printf("PASS: OpenSSL=%s; zlib=%s; SHA256 bytes=%u; compression round-trip OK\n",
           OpenSSL_version(OPENSSL_VERSION), zlibVersion(), digest_len);
    return 0;
}
'''

# Narrow prototype export contract, not a claim to support every Nix setup hook.
# Keep compiler-wrapper variables and common native build discovery variables.
ACTIVATION_NAMES = {
    "PATH", "CC", "CXX", "CPP", "AR", "AS", "LD", "NM", "RANLIB", "STRIP",
    "OBJCOPY", "OBJDUMP", "READELF", "SIZE", "STRINGS", "PKG_CONFIG",
    "PKG_CONFIG_PATH", "PKG_CONFIG_LIBDIR", "CMAKE_PREFIX_PATH",
    "CMAKE_INCLUDE_PATH", "CMAKE_LIBRARY_PATH", "ACLOCAL_PATH",
}


class Experiment:
    def __init__(self, container, output):
        self.container = container
        self.output = output
        self.manager_calls = 0

    def run(self, args, *, check=True):
        started = time.monotonic()
        result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=300)
        with (self.output / "commands.jsonl").open("a") as log:
            log.write(json.dumps({"command": args, "returncode": result.returncode,
                                  "seconds": round(time.monotonic() - started, 3),
                                  "stdout": result.stdout.decode(errors="replace"),
                                  "stderr": result.stderr.decode(errors="replace")}) + "\n")
        if check and result.returncode:
            raise RuntimeError(f"Command failed ({result.returncode}): {args[:8]}\n"
                               + result.stderr.decode(errors="replace")[-6000:])
        return result

    def execute(self, args, *, env=None, check=True):
        # An explicit environment is clean, not inherited from the worker/container.
        command = ["docker", "exec", self.container]
        if env is not None:
            command += ["env", "-i"] + [f"{k}={v}" for k, v in sorted(env.items())]
        return self.run(command + args, check=check)

    def nix(self, *args, check=True):
        self.manager_calls += 1
        # Deliberately hostile ambient settings: command-line policy must win.
        return self.run(["docker", "exec", "-e",
                         "NIX_CONFIG=max-jobs = 1\nbuilders = ssh://c2j-no-build.invalid\n",
                         self.container] + NIX + list(args), check=check)

    def describe(self, selector):
        document = json.loads(self.nix("derivation", "show", selector).stdout)
        assert document["version"] == 4, "Prototype expects the pinned Nix image's format"
        assert len(document["derivations"]) == 1
        name, drv = next(iter(document["derivations"].items()))
        assert not drv["env"].get("__structuredAttrs"), "Structured attrs need separate support"
        return "/nix/store/" + name, drv

    def realize_inputs(self, drv, roots):
        self.execute(["mkdir", "-p", roots])
        refs = []
        for name, inputs in sorted(drv["inputs"]["drvs"].items()):
            assert not inputs["dynamicOutputs"], "Dynamic derivations are outside this prototype"
            refs.append("/nix/store/" + name + "^" + ",".join(inputs["outputs"]))
        # Build ONLY the selected inputs, never the generated shell derivation.
        # Nix retains out-links/GC roots for each selected output and its closure.
        return self.nix("build", "--out-link", roots + "/input", "--", *refs, check=False)

    def activate(self, drv, prefix):
        scratch = prefix + "/capture"
        self.execute(["mkdir", "-p", scratch])
        env = dict(drv["env"])
        env.update({"HOME": scratch, "TMPDIR": scratch, "NIX_BUILD_TOP": scratch,
                    "out": scratch + "/unused-output", "IN_NIX_SHELL": "impure",
                    "NIX_NO_SELF_RPATH": "1", "NIX_ENFORCE_PURITY": "0"})
        result = self.execute([drv["builder"], "--noprofile", "--norc", "-c",
                               'cd "$NIX_BUILD_TOP"; source "$stdenv/setup" >&2; env -0'], env=env)
        exported = dict(entry.split("=", 1) for entry in result.stdout.decode().split("\0") if entry)
        activation = {k: v for k, v in exported.items()
                      if k in ACTIVATION_NAMES or (k.startswith("NIX_") and k != "NIX_BUILD_TOP")}
        for k, v in activation.items():
            assert scratch not in v, f"Temporary capture path leaked through {k}"
        assert "LD_LIBRARY_PATH" not in activation
        assert "HOME" not in activation
        assert "PKG_CONFIG_PATH" in activation
        assert "NIX_CFLAGS_COMPILE" in activation
        assert "NIX_LDFLAGS" in activation
        assert "-openssl-" in activation["PKG_CONFIG_PATH"]
        assert "-zlib-" in activation["PKG_CONFIG_PATH"]
        self.execute(["rm", "-rf", scratch])
        return activation

    def probe(self, activation, prefix, name):
        env = dict(activation, HOME=prefix, TMPDIR="/tmp")
        script = ('set -eu; cd "$HOME"; '
                  'pkg-config --modversion openssl zlib; '
                  'cc probe.c -o ' + name + ' $(pkg-config --cflags --libs openssl zlib); '
                  './' + name)
        out = self.execute(["/bin/sh", "-c", script], env=env).stdout.decode()
        assert "compression round-trip OK" in out
        return out


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", help="Reuse a running disposable Nix container (not removed)")
    parser.add_argument("--output-dir", type=Path, help="Retain logs and activation.json here")
    args = parser.parse_args()
    output = args.output_dir or Path(tempfile.mkdtemp(prefix="c2j-native-results-"))
    output.mkdir(parents=True, exist_ok=True)
    container = args.container or "c2j-native-" + uuid.uuid4().hex[:12]
    experiment = Experiment(container, output)
    prefix = "/tmp/c2j-native-" + uuid.uuid4().hex[:12]
    report = {"image": IMAGE, "nixpkgs_revision": REVISION, "checks": {}}
    started = time.monotonic()
    try:
        if not args.container:
            experiment.run(["docker", "run", "-d", "--name", container,
                            "--entrypoint", "/bin/sh", IMAGE, "-c", "sleep 3600"])
        system = experiment.nix("eval", "--impure", "--raw", "--expr", "builtins.currentSystem").stdout.decode()
        report["system"] = system
        with tempfile.TemporaryDirectory(prefix="c2j-native-fixtures-") as fixture:
            Path(fixture, "flake.nix").write_text(FLAKE)
            Path(fixture, "probe.c").write_text(PROBE)
            experiment.run(["docker", "cp", fixture, container + ":" + prefix])
        selector = prefix + "#devShells." + system + ".default"
        print("Evaluating the consumer shell and realizing only its input outputs...", flush=True)
        drv_path, drv = experiment.describe(selector)
        (output / "derivation.json").write_text(json.dumps(drv, indent=2) + "\n")
        result = experiment.realize_inputs(drv, prefix + "/roots")
        assert result.returncode == 0, result.stderr.decode()[-6000:]
        report["checks"]["substitution_only_inputs"] = True
        print("Checking that print-dev-env cannot run unchanged under max-jobs=0...", flush=True)
        result = experiment.nix("print-dev-env", "--json", selector, check=False)
        assert result.returncode != 0 and b"max-jobs" in result.stderr, result.stderr.decode()
        report["checks"]["print_dev_env_requires_capture_build"] = True
        print("Capturing native setup hooks without building the shell...", flush=True)
        before = experiment.manager_calls
        activation = experiment.activate(drv, prefix)
        assert experiment.manager_calls == before
        shell_output = "/nix/store/" + drv["outputs"]["out"]["path"]
        assert experiment.execute(["test", "-e", shell_output], check=False).returncode != 0
        manifest = {"schema": "prototype-v1", "system": system, "shell_derivation": drv_path,
                    "environment": activation}
        (output / "activation.json").write_text(json.dumps(manifest, indent=2) + "\n")
        report["checks"]["activation_without_shell_build"] = True
        report["cold_probe"] = experiment.probe(activation, prefix, "probe-cold")
        print(report["cold_probe"], end="", flush=True)
        report["direct_link_probe"] = experiment.execute(
            ["/bin/sh", "-c", 'set -eu; cd "$HOME"; '
             'cc probe.c -o probe-direct -lcrypto -lz; ./probe-direct'],
            env=dict(activation, HOME=prefix, TMPDIR="/tmp")).stdout.decode()
        assert "compression round-trip OK" in report["direct_link_probe"]
        report["checks"]["compiler_hooks_without_pkg_config_flags"] = True
        # Run the dynamically linked output with no activation variables at all.
        report["runtime_without_activation"] = experiment.execute(
            [prefix + "/probe-cold"], env={"PATH": "/no-tools"}).stdout.decode()
        assert "compression round-trip OK" in report["runtime_without_activation"]
        report["checks"]["runtime_without_ld_library_path"] = True
        # zlib development output has no executable directory, and is still usable.
        zlib_dev = next(p for p in drv["env"]["buildInputs"].split() if "-zlib-" in p)
        assert experiment.execute(["test", "-d", zlib_dev + "/bin"], check=False).returncode != 0
        report["checks"]["library_without_bin"] = True
        print("Testing non-substitutable inputs, including preferLocalBuild=true...", flush=True)
        for name in ("missing", "missingLocal"):
            _, bad = experiment.describe(prefix + "#devShells." + system + "." + name)
            result = experiment.realize_inputs(bad, prefix + "/roots-" + name)
            assert result.returncode != 0 and b"max-jobs" in result.stderr, result.stderr.decode()
            assert b"SOURCE_BUILD_ATTEMPTED" not in result.stderr
            report["checks"][name + "_rejected_without_build"] = True
        print("Reloading activation from disk and recompiling without Nix on PATH...", flush=True)
        before = experiment.manager_calls
        warm = json.loads((output / "activation.json").read_text())["environment"]
        assert experiment.execute(["/bin/sh", "-c", "command -v nix"], env=warm, check=False).returncode != 0
        report["warm_probe"] = experiment.probe(warm, prefix, "probe-warm")
        assert experiment.manager_calls == before
        report["checks"]["warm_no_manager_calls"] = True
        # Delete unrooted store paths; inputs required by saved activation must survive.
        print("Garbage-collecting unrooted outputs and checking retained activation...", flush=True)
        experiment.nix("store", "gc")
        report["after_gc_probe"] = experiment.probe(warm, prefix, "probe-after-gc")
        report["checks"]["retained_outputs_survive_gc"] = True
        # Setup must not have changed the container's ambient environment.
        assert experiment.execute(["/bin/sh", "-c", 'test -z "${PKG_CONFIG_PATH:-}"']).returncode == 0
        report["checks"]["no_ambient_environment_mutation"] = True
        report["seconds"] = round(time.monotonic() - started, 3)
        (output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2), flush=True)
        print("Evidence: " + str(output.resolve()), flush=True)
    finally:
        if not args.container:
            subprocess.run(["docker", "rm", "-f", container], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    main()
