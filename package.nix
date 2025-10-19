{
  lib,
  buildGoModule,
}:

let
  name = "kara";
  version = "0.0.1";
in
buildGoModule {
  inherit version;
  pname = name;
  src = ./.;
  subPackages = [
    "cmd/kara"
  ];

  # Fully static binary (like the root Dockerfile), so it runs on a scratch base image.
  env.CGO_ENABLED = "0";

  # SRI hash of the vendored module dependencies (go mod vendor).
  vendorHash = "sha256-RYeSAy0QrFdw4Nz0y7LLJ35ZkfoPxX9cMRv5Tpq1E7c=";

  meta = {
    mainProgram = "kara";
    description = "Kubernetes Azure DevOps RunnerPool Autoscaler";
    homepage = "https://kara.felukka.org";
    platforms = lib.platforms.unix;
  };
}
