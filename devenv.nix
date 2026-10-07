{ pkgs, config, inputs, ... }:
let
  pkgs-unstable = import inputs.nixpkgs-unstable { system = pkgs.stdenv.system; };
  lint = pkgs.writeShellApplication {
    name = "graphene-lint";
    runtimeInputs = [
      pkgs.findutils
      pkgs.go
      pkgs-unstable.gotools
      pkgs-unstable.go-tools
    ];
    text = ''
      lint_cache_root="''${TMPDIR:-/tmp}/graphene-lint-cache"
      export XDG_CACHE_HOME="''${XDG_CACHE_HOME:-$lint_cache_root}"
      export STATICCHECK_CACHE="''${STATICCHECK_CACHE:-$lint_cache_root/staticcheck}"
      mkdir -p "$XDG_CACHE_HOME" "$STATICCHECK_CACHE"

      unformatted="$({
        find . \
          \( -path './.git' -o -path './.devenv' -o -path './.direnv' \) -prune \
          -o -type f -name '*.go' -print0
      } | xargs -0 -r gofmt -l)"
      if [ -n "$unformatted" ]; then
        printf 'The following Go files are not formatted:\n%s\n' "$unformatted" >&2
        exit 1
      fi

      go mod tidy -diff
      go vet ./...
      staticcheck ./...
      modernize ./...
      go fix -diff ./...
    '';
  };
in
  {
    profiles = {
      # Keep development-only tools out of the closure materialized in CI.
      dev.module =
        let
          agents = import inputs.agents { inherit pkgs; };
        in
          { pkgs, ... }:
          {
            languages.go.enable = true;

            packages = [
              pkgs.git
              pkgs-unstable.gh
              agents.codex
            ];
          };
      ci.module = { pkgs, ... }: {
        packages = [
          pkgs.git
          pkgs.go
          pkgs.zsh
        ];
        env.GOTOOLCHAIN = "local";
      };
    };

    scripts = {
      p-test-unit.exec = "go test -parallel 8 -run '^TestUnit' ./internal/...";
      p-test-integration.exec = "go test -parallel 8 -skip '^TestUnit' ./internal/...";
      p-test-e2e.exec = "go test -count=1 -parallel 8 ./tests/e2e";
      p-test.exec = ''
        set -euo pipefail

        p-test-unit
        p-test-integration
        p-test-e2e
      '';
      p-build.exec = "go build -o bin/graphene ./cmd/graphene";
      p-lint.exec = "${lint}/bin/graphene-lint";
      p-ci.exec = ''
        set -euo pipefail

        p-lint
        p-test
        if [[ "''${NO_BUILD:-}" != "1" ]]; then
          p-build
        fi
      '';
    };

    outputs = {
      graphene = pkgs.callPackage ./package.nix { };
    };
  }
