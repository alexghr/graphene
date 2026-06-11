{ pkgs, lib, config, inputs, ... }:
let
  pkgs-unstable = import inputs.nixpkgs-unstable { system = pkgs.stdenv.system; };
  version = lib.removeSuffix "\n" (builtins.readFile ./VERSION);
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
        nativeCheckInputs = [ pkgs.git ];
        subPackages = [ "cmd/graphene" ];
        postInstall = ''
          install -Dm644 aliases/graphite.gitconfig $out/share/graphene/aliases/graphite.gitconfig
        '';
        checkPhase = ''
          runHook preCheck
          go test -parallel 8 ./...
          runHook postCheck
        '';
      };
    };
  }
