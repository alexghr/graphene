{ pkgs, lib, config, inputs, ... }:
let
  pkgs-unstable = import inputs.nixpkgs-unstable { system = pkgs.stdenv.system; };
  version = lib.removeSuffix "\n" (builtins.readFile ./VERSION);
  lint = pkgs.writeShellApplication {
    name = "graphene-lint";
    runtimeInputs = [
      pkgs.findutils
      pkgs.go
      pkgs-unstable.gotools
      pkgs-unstable.go-tools
    ];
    text = ''
      export XDG_CACHE_HOME="''${XDG_CACHE_HOME:-''${TMPDIR:-/tmp}/graphene-lint-cache}"
      mkdir -p "$XDG_CACHE_HOME"

      unformatted="$({
        find . \
          \( -path './.git' -o -path './.devenv' -o -path './.direnv' \) -prune \
          -o -type f -name '*.go' -print0
      } | xargs -0 -r gofmt -l)"
      if [ -n "$unformatted" ]; then
        printf 'The following Go files are not formatted:\n%s\n' "$unformatted" >&2
        exit 1
      fi

      go vet ./...
      staticcheck ./...
      modernize ./...
      go fix -diff ./...
    '';
  };
in
  {
    packages = with pkgs;
      [
        git
        pkgs-unstable.codex
        pkgs-unstable.gh
      ];

    languages.go = {
      enable = true;
    };

    scripts = {};

    scripts = {
      graphene-test.exec = "go test -parallel 8 ./...";
      graphene-build.exec = "go build -o bin/graphene ./cmd/graphene";
      graphene-lint.exec = "${lint}/bin/graphene-lint";
    };

    outputs = {
      graphene = pkgs.buildGoModule rec {
        pname = "graphene";
        inherit version;
        src = lib.cleanSource ./.;
        vendorHash = null;
        env.CGO_ENABLED = "0";
        ldflags = [
          "-X github.com/alexghr/graphene/internal/graphene.Version=${version}"
        ];
        nativeCheckInputs = [
          pkgs.git
          pkgs.zsh
          lint
        ];
        subPackages = [ "cmd/graphene" ];
        postInstall = ''
          install -Dm644 aliases/graphite.gitconfig $out/share/graphene/aliases/graphite.gitconfig
          install -Dm644 internal/graphene/graphene.bash $out/share/bash-completion/completions/graphene
          install -Dm644 internal/graphene/graphene.bash $out/share/bash-completion/completions/gn
          install -Dm644 internal/graphene/_graphene $out/share/zsh/site-functions/_graphene
        '';
        checkPhase = ''
          runHook preCheck
          graphene-lint
          go test -parallel 8 ./...
          runHook postCheck
        '';
      };
    };
  }
