# Prerequisites

## Kubernetes cluster

- Kubernetes **1.28+** (the operator is built and tested against the 1.32 API).
- A persistent volume provisioner is optional but recommended if you want per-agent [storage](../resources/runnerpool.md#storage).
- `kubectl` configured to talk to the cluster.

## cert-manager

The admission webhooks use [cert-manager](https://cert-manager.io) to issue a serving certificate and to inject its CA into the webhook configurations. Install it before the operator:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.0/cert-manager.yaml
```

> Without cert-manager you can still install the operator, but set `webhook.enabled=false` / `certManager.enabled=false` (Helm) so no webhook resources are created. Validation and defaulting will then be inactive.

## Azure DevOps

- An Azure DevOps organization (cloud or on-premises Server/TFS) and an **agent pool** in it.
- A **personal access token (PAT)** with:
  - **Agent pools**: Read, Manage — to list the pool, register/unregister agents.
  - **Build**: Read, Invoke — to read the queue (job requests) of the pool.

Create a token under *User settings → Personal access tokens* (fine-grained, scoped to the project if you only want one project).

> !!! warning
> KARA stores the PAT reference, not the PAT. The token itself lives in a Kubernetes Secret (see [Quickstart](quickstart.md)). Never commit the token to the repository.

## Agent image

- A container image that runs the Azure DevOps agent. KARA provides one: [`ghcr.io/mahmoudk1000/kara/agent`](#building-the-agent-image).
  - `base` — Ubuntu 22.04, Azure DevOps agent 4.259.0, `kubectl`, `helm`, `yq`.
  - `java` — base + OpenJDK 17, Maven.
  - `dotnet` — base + .NET SDK 8.
  - `nodejs` — base + Node.js 20, Chromium.

  Variants are selected at build time with `--build-arg RUNTIME_TYPE=...` and advertise themselves through the `AZP_CAPABILITY` environment variable.
- Your cluster must be able to pull the image (registry credentials if it is private).

### Building the agent image

```bash
# from the repository root
make agent-build AGENT_IMG=<registry>/kara/agent:tag RUNTIME_TYPE=base
```

Or directly:

```bash
docker build -t <registry>/kara/agent:tag --build-arg RUNTIME_TYPE=base agent/
docker push <registry>/kara/agent:tag
```

## Network

Agent pods need outbound access to the Azure DevOps organization URL (`azUrl`) and to the container registry. If you use [network policies](../configuration/security.md#network-policies), allow egress to both.
