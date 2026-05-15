{ pkgs, lib, config, inputs, ... }:
let
  pkgs-unstable = import inputs.nixpkgs-unstable { system = pkgs.stdenv.system; };
in
  {
    packages = with pkgs;
      [
        git
        watch
        tmux
        pkgs-unstable.codex
      ];

    languages.go = {
      enable = true;
    };

    scripts = {};

    scripts = {
      graphene-test.exec = "go test ./...";
      graphene-build.exec = "go build -o bin/graphene ./cmd/graphene";
    };

    outputs = {
      graphene = pkgs.buildGoModule rec {
        pname = "graphene";
        version = "0.5.1";
        src = lib.cleanSource ./.;
        vendorHash = null;
        ldflags = [
          "-X github.com/alexghr/graphene/internal/graphene.Version=${version}"
        ];
        nativeCheckInputs = [ pkgs.git ];
        subPackages = [ "cmd/graphene" ];
        checkPhase = ''
          runHook preCheck
          go test ./...
          runHook postCheck
        '';
      };
    };
  }
