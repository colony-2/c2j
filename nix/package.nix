{ lib, buildGoModule, tzdata, iana-etc, mailcap, version, revision }:
buildGoModule {
  pname = "c2j";
  inherit version;

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      (lib.fileset.fileFilter (file:
        file.hasExt "go" && !(lib.hasSuffix "_test.go" file.name)
      ) ../cmd)
      (lib.fileset.fileFilter (file:
        file.hasExt "go" && !(lib.hasSuffix "_test.go" file.name)
      ) ../pkg)
      (lib.fileset.fileFilter (file:
        file.hasExt "go" && !(lib.hasSuffix "_test.go" file.name)
      ) ../internal)
      # Text resources embedded in the executable, not an npm installation.
      ../skills
    ];
  };
  vendorHash = "sha256-eH0q8Cg+Qqvwux2Zd0PkbDA20ezxdIDPXjrR0doURmI=";
  subPackages = [ "cmd/c2j" ];
  env.CGO_ENABLED = "0";
  ldflags = [ "-s" "-w" "-X github.com/colony-2/c2j/internal/buildinfo.version=${version}" "-X github.com/colony-2/c2j/internal/buildinfo.commit=${revision}" ];
  # The full Go suite runs in the existing CI test job before publication.
  doCheck = false;

  # Nixpkgs Go references these standard-library data files. No interpreter,
  # shell wrapper, or external tool may enter the executable's runtime closure.
  allowedReferences = [ tzdata iana-etc mailcap ];
  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck
    test "$(find "$out" -type f -o -type l)" = "$out/bin/c2j"
    test "$("$out/bin/c2j" version)" = "c2j version ${version}"
    "$out/bin/c2j" --help >/dev/null
    runHook postInstallCheck
  '';

  meta = {
    description = "CLI for submitting and running recipe jobs through JobDB";
    homepage = "https://github.com/colony-2/c2j";
    license = lib.licenses.asl20;
    mainProgram = "c2j";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}
