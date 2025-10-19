# Quickstart

This guide scales an Azure DevOps pool with KARA. It assumes the [operator is installed](installation.md).

You need:

- An Azure DevOps organization and agent pool (see [Prerequisites](prerequisites.md)).
- A PAT with *Agent pools (Read, Manage)* and *Build (Read, Invoke)*.
- An agent image you can pull (the default is `ghcr.io/mahmoudk1000/kara/agent:latest`).

## 1. Create a namespace and the PAT Secret

```bash
kubectl create namespace ci

kubectl -n ci create secret generic kara-pat \
  --from-literal=token="<your-pat>"
```

KARA reads the token from this Secret at runtime; it never sees the token in the resource.

## 2. Create the RunnerPool

Save this as `runnerpool.yaml` and change the organization, pool and image:

```yaml
apiVersion: kara.felukka.org/v1
kind: RunnerPool
metadata:
  name: default
  namespace: ci
spec:
  azUrl: "https://dev.azure.com/<your-org>"
  pool: "Default"
  patSecretName: "kara-pat"
  image: "ghcr.io/mahmoudk1000/kara/agent:latest"
  minAgents: 0
  maxAgents: 5
  ttlIdleSeconds: 300
```

```bash
kubectl apply -f runnerpool.yaml
```

> The mutating webhook fills in defaults for anything you leave empty (pool, image, pull policy, TTL, agent bounds). See [RunnerPool](../resources/runnerpool.md#defaults) for the full list.

## 3. Watch it connect

```bash
kubectl -n ci get runnerpool -w
```

Within one poll interval the status flips to connected and a warm agent appears (if you set `minAgents`):

```
NAME      STATUS       POOL     ORGANIZATION   QUEUED   AGENTS   RUNNING
default   Connected    Default  your-org       0        0/5      0
```

!!! tip "One agent is a pod"
Each row of the agent table is a Kubernetes pod in the same namespace, named `<pool>-agent-<index>`:

```bash
kubectl -n ci get pods -w
```

## 4. Queue a job and watch it scale

Queue a build or pipeline run against the pool in Azure DevOps. On the next poll KARA notices the queued job request, creates an agent pod, and the agent registers itself with the pool:

```
NAME      STATUS       POOL     ORGANIZATION   QUEUED   AGENTS   RUNNING
default   Connected    Default  your-org       1        1/5      1
```

The job runs on the new pod. Repeat the step a few times — KARA adds one pod per queued job until it hits `maxAgents`.

## 5. Watch it scale down

When the jobs finish and no new work arrives, each agent becomes idle. After `ttlIdleSeconds` (300 s by default) KARA unregisters the agent and deletes its pod:

```
NAME      STATUS       POOL     ORGANIZATION   QUEUED   AGENTS   RUNNING
default   Connected    Default  your-org       0        0/5      0
```

## 6. Keep some agents warm

```yaml
spec:
  minAgents: 1
```

`kubectl apply` it and the pool keeps one warm agent around the clock. The first job then starts in seconds instead of minutes.

## 7. Clean up

```bash
kubectl -n ci delete runnerpool default
kubectl -n ci delete secret kara-pat
kubectl delete namespace ci
```

Deleting the RunnerPool unregisters every KARA-managed agent from the pool and deletes its pods and PVCs before the resource goes away.
