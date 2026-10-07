{ lib, buildGoModule }:

buildGoModule rec {
  pname = "graphene";
  version = lib.removeSuffix "\n" (builtins.readFile ./VERSION);
  src = lib.cleanSource ./.;
  vendorHash = null;
  env.CGO_ENABLED = "0";
  ldflags = [
    "-X github.com/alexghr/graphene/internal/graphene.Version=${version}"
  ];
  doCheck = false;
  subPackages = [ "cmd/graphene" ];
  postInstall = ''
    install -Dm644 aliases/graphite.gitconfig $out/share/graphene/aliases/graphite.gitconfig
    install -Dm644 internal/graphene/graphene.bash $out/share/bash-completion/completions/graphene
    install -Dm644 internal/graphene/graphene.bash $out/share/bash-completion/completions/gn
    install -Dm644 internal/graphene/_graphene $out/share/zsh/site-functions/_graphene
  '';
}
