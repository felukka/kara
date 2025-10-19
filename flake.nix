{
  description = "Kubernetes Azure DevOps RunnerPool Autoscaler";

  inputs.nixpkgs.url = "nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      inherit (nixpkgs) lib;

      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];

      overlay = final: _: {
        kara = final.callPackage ./package.nix { };
      };

      pkgsFor = lib.genAttrs systems (
        system:
        import nixpkgs {
          inherit system;
          overlays = [ overlay ];
        }
      );

      forAllSystems = f: lib.genAttrs systems (system: f pkgsFor.${system});
    in
    {
      overlays.default = overlay;

      packages = forAllSystems (pkgs: {
        default = pkgs.kara;

        # Docker image for the operator, built entirely in nix:
        # the static Go binary (packages.kara) layered onto a scratch base.
        # Mirrors the root Dockerfile: entrypoint /manager, non-root user 65532.
        docker = pkgs.dockerTools.buildImage {
          name = "kara";
          tag = "0.1.0";
          copyToRoot = pkgs.runCommand "kara-image-root" { }
            ''
              mkdir -p $out
              install -Dm755 ${pkgs.kara}/bin/kara $out/manager
            '';
          config = {
            User = "65532";
            ExposedPorts = {
              "8081/tcp" = { }; # health probes
              "8443/tcp" = { }; # metrics
              "9443/tcp" = { }; # webhooks
            };
            Entrypoint = [ "/manager" ];
          };
        };
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShellNoCC {
          buildInputs = with pkgs; [
            gcc
            gnumake
            go
            gofumpt
            goimports-reviser
            golangci-lint
            golines
            gopls
            go-tools
            gotools
            kubebuilder
            (python3.withPackages (
              p: with p; [
                mkdocs-material
              ]
            ))
          ];
        };
      });

    };
}
