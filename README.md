# KARA (Kubernetes Azure Runner Autoscaler)

A Kubernetes operator built in Go to automatically scale Azure DevOps self-hosted agents based on pipeline workload.

## Description

KARA (Kubernetes Azure Runner Autoscaler) bridges the gap between Azure DevOps (ADO) and Kubernetes. By continuously monitoring ADO agent pool queues, KARA dynamically provisions and de-provisions self-hosted agent pods within your Kubernetes cluster.

Instead of maintaining a static pool of VMs or running agents that sit idle, this operator ensures you always have exactly the compute you need. It drastically reduces CI/CD pending times during peak deployment hours while scaling down to zero to save infrastructure costs when queues are empty.

## Features

- **Autoscaling on demand** — polls the Azure DevOps agent pool's queued job requests and spawns agent pods to cover the queue, respecting `maxAgents`.
- **Minimum agents** — keeps `minAgents` warm agents around so the first job doesn't wait for a cold start. A minimum agent never scales to zero.
- **Capability-aware scaling** — queued jobs with demands (e.g. Docker images) are only matched to agents that carry the corresponding `capabilityImages`, so a job demanding `ubuntu-latest` only scales agents that run it.
- **Job-level deduplication** — one pod per distinct capability set in the queue; duplicate jobs for the same capability don't spawn duplicate agents.
- **Idle scale-down** — agents that finish their work and stay idle for `ttlIdleSeconds` are unregistered from the pool and their pods removed.
- **Completed-job retention** — pods that just finished a job are kept for the idle TTL window so logs and re-runs stay available, then cleaned up.
- **Structured storage** — optional persistent volumes per agent (PVCs), with an init container that prepares the workspace. PVCs are owned by the RunnerPool and cleaned up on deletion.
- **One-shot agents** — non-minimum agents with `ttlIdleSeconds: 0` run with `--once` and unregister themselves when idle, ideal for spot/ephemeral capacity.
- **Safe finalizer cleanup** — deleting a RunnerPool unregisters all operator-managed agents from the pool and deletes their pods and PVCs before the CR goes away.
- **Health checks** — offline agents that the pool no longer reports but whose pods still exist are reaped; error-state pods are cleaned up.
- **Admission webhooks** — a mutating webhook defaults sensible values and a validating webhook enforces the invariants (`minAgents <= maxAgents`, `ttlIdleSeconds >= 0`, ...).
- **Rich status** — `status` tracks pool state, live/online agent counts, the index→agent mapping, per-reason condition transitions, and last-observed polling info.
- **Metrics** — Prometheus metrics (agent counts, queued jobs, reconciliations) exposed on a secured, authenticated endpoint.

## API

The operator manages `RunnerPool` resources in the `kara.felukka.org/v1` API group.

```yaml
apiVersion: kara.felukka.org/v1
kind: RunnerPool
metadata:
  name: my-pool
  namespace: default
spec:
  azUrl: "https://dev.azure.com/my-org"        # Azure DevOps organization URL
  pool: "Default"                              # agent pool name (required)
  patSecretName: "kara-pat"                    # secret with the PAT token (required)
  patSecretKey: "token"                        # key in the secret (default "token")
  image: "ghcr.io/mahmoudk1000/kara/agent:latest"  # agent image (required)
  maxAgents: 5
  minAgents: 0
  ttlIdleSeconds: 300
  pollIntervalSeconds: 30
  nodeSelector: {}
  affinity: {}
  tolerations: []
  resources: {}                                # CPU/memory for agent pods
  capabilityImages: {}                         # demand -> image, e.g. {"ubuntu-latest": "ghcr.io/.../agent:ubuntu"}
  storage:
    size: "10Gi"
    storageClass: ""                           # empty = cluster default
    name: "agent-data"                         # suffix used in PVC names
```

Create the PAT secret first:

```sh
kubectl create secret generic kara-pat --from-literal=token="<your-pat>"
```

## Getting Started

### Prerequisites

- go version v1.23.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster

**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/kara:tag

```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install

```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/kara:tag

```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
> privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/

```

> **NOTE**: Ensure that the samples have default values to test them out.

### To Uninstall

**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/

```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall

```

**UnDeploy the controller from the cluster:**

```sh
make undeploy

```

## Project Distribution

Both distribution formats are produced by a single target:

```sh
make dist
```

This writes:

- `dist/install.yaml` — the full Kustomize bundle (CRDs, RBAC, deployment, services, webhooks, cert-manager resources) for `kubectl apply`.
- `dist/kara-<version>.tgz` — the Helm chart package.

### By providing a bundle with all YAML files

1. Build the installer (set `IMG` first so the bundle references your image):

```sh
make dist IMG=<some-registry>/kara:tag
```

**NOTE:** `dist/install.yaml` contains all the resources built with Kustomize,
which are necessary to install this project without its dependencies.

1. Using the installer

Users can just run `kubectl apply -f <URL for YAML BUNDLE>` to install
the project, i.e.:

```sh
kubectl apply -f [https://raw.githubusercontent.com/](https://raw.githubusercontent.com/)<org>/kara/<tag or branch>/dist/install.yaml

```

### By providing a Helm Chart

A Helm chart lives in [`chart/kara`](chart/kara/). It installs the CRD,
RBAC, the operator deployment, the metrics and webhook services, and the
cert-manager issuer/certificate that powers the admission webhooks.

1. Install the chart (a [cert-manager](https://cert-manager.io) installation
   is required in the cluster):

```sh
helm install kara chart/kara \
  --namespace kara-system --create-namespace \
  --set image.repository=<some-registry>/kara \
  --set image.tag=tag
```

2. Or install from a packaged chart:

```sh
make dist
helm install kara dist/kara-<version>.tgz \
  --namespace kara-system --create-namespace \
  --set image.repository=<some-registry>/kara \
  --set image.tag=tag
```

Useful values: `replicaCount`, `image.repository`, `image.tag`,
`leaderElection.enabled`, `webhook.enabled`, `certManager.enabled`,
`metrics.enabled`, `resources`, `nodeSelector`, `affinity`, `tolerations`.

**NOTE:** If you change the project (CRD schema, RBAC, webhooks), update the
corresponding files in `chart/kara/` and bump the chart `version` before
re-packaging with `make dist`.

## Contributing

Contributions are what make the open-source community such an amazing place to learn, inspire, and create. Any contributions you make are **greatly appreciated**.

If you have a suggestion that would make KARA better, please fork the repo and create a pull request. You can also simply open an issue with the tag "enhancement".

1. Fork the Project
2. Create your Feature Branch (`git checkout -b feature/AmazingFeature`)
3. Commit your Changes (`git commit -m 'Add some AmazingFeature'`)
4. Push to the Branch (`git push origin feature/AmazingFeature`)
5. Open a Pull Request

**NOTE:** Run `make help` for more information on all potential `make` targets.

More information on how operators work can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html).

## License

Copyright 2026 mahmoudk1000.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

```
http://www.apache.org/licenses/LICENSE-2.0
```

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
