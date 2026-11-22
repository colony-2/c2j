{
  # No nixpkgs input: only the disposable producer builds these fixtures.
  outputs = { self }: {
    packages = builtins.listToAttrs (map (system: {
      name = system;
      value = let
        common = {
          inherit system;
          builder = "/bin/sh";
          PATH = "/nix/var/nix/profiles/default/bin:/usr/bin:/bin";
          allowSubstitutes = false;
          preferLocalBuild = true;
        };
        payload = builtins.derivation (common // {
          name = "c2j-substitution-payload";
          # The old-request control must fail on the wrapper's own policy,
          # rather than on an unsubstitutable input derivation.
          allowSubstitutes = true;
          preferLocalBuild = false;
          args = [ "-c" ''
            echo attempted > /tmp/c2j-local-build-attempted
            mkdir -p "$out"
            printf '#!/bin/sh\necho signed-closure-ok\n' > "$out/run"
            chmod +x "$out/run"
          '' ];
        });
        wrapper = builtins.derivation (common // {
          name = "c2j-substitution-wrapper";
          outputs = [ "out" "dev" "bin" "lib" ];
          args = [ "-c" ''
            echo attempted > /tmp/c2j-local-build-attempted
            mkdir -p "$out" "$dev" "$bin/bin" "$lib"
            printf '#!/bin/sh\nexec ${payload}/run "$@"\n' > "$bin/bin/c2j-wrapper"
            chmod +x "$bin/bin/c2j-wrapper"
            for output in "$out" "$dev" "$lib"; do
              printf '%s\n' '${payload}' > "$output/payload"
            done
          '' ];
        });
        ifd = builtins.derivation (common // {
          name = "c2j-forbidden-ifd";
          args = [ "-c" ''
            echo attempted > /tmp/c2j-ifd-builder-attempted
            echo '{}' > "$out"
          '' ];
        });
      in {
        default = wrapper;
        inherit payload;
        # Two names select the same path; request/root dedup must preserve bin.
        alias = wrapper // { lib = wrapper.dev; };
        forbidden = (import ifd) // wrapper;
      };
    }) [ "aarch64-linux" "x86_64-linux" ]);
  };
}
