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
      graphene = pkgs.buildGoModule {
        pname = "graphene";
        version = "0.1.0";
        src = lib.cleanSource ./.;
        vendorHash = null;
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
