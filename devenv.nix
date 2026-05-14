{ pkgs, ... }:

{
  packages = [
    pkgs.go
    pkgs.gotools
    pkgs.golangci-lint
  ];
}
