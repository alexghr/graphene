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

    outputs = {};
  }
