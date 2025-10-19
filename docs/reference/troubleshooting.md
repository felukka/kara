# Troubleshooting

Start with the resource status and the operator logs:

```bash
kubectl -n ci get runnerpool -w
kubectl -n ci describe runnerpool <name>
kubectl -n kara-system logs deploy/kara-controller-manager --tail=100
```

## The pool never connects

**Symptoms:** `status.connectionStatus` is `Disconnected` or `Error`, `status.lastError` is set.

- **PAT invalid or expired** — the most common cause. Re-create the Secret and check `lastError` (it will mention the Azure DevOps response). Rotate by editing the Secret in place.
- **Insufficient PAT scopes** — the token needs *Agent pools (Read, Manage)* and *Build (Read, Invoke)*.
- **Wrong `azUrl`** — it must be the organization root (e.g. `https://dev.azure.com/<org>`), not a project or collection URL.
- **Wrong pool name** — `spec.pool` must match the pool name in Azure DevOps exactly (case-insensitive, but the `Pool` column in the status shows what was resolved).
- **No network egress** — the operator pod must reach the `azUrl` host on 443. Check NetworkPolicies and egress proxies.

## Agents are created but jobs never start

**Symptoms:** pods exist, `runningAgents` > 0, but `queuedJobs` stays > 0.

- Check the agent pod logs — the registration step prints the Azure DevOps response:

  ```bash
  kubectl -n ci logs <pool>-agent-<index> --tail=100
  ```

- **Image cannot pull** — `kubectl describe pod` shows `ImagePullBackOff`. Fix registry credentials or use a public image.
- **Agent not reaching the org** — the agent pod also needs egress to `azUrl`.
- **Capability mismatch** — in capability-aware mode, a job demanding a capability that has no `capabilityImages` entry will not scale a matching agent. Check `status.queuedJobsByCap`.

## Agents are not scaling down

- **`minAgents`** — at least `minAgents` agents always stay. Lower it to 0 to scale to zero.
- **Idle TTL** — agents are kept for `ttlIdleSeconds` after their last job. Lower it (minimum 0 = one-shot) to reclaim faster.
- **A job is still running** — an agent with an active/running request is never idle. Check `status.agents[].status`.
- **Completed retention** — a pod that just finished a job is kept for the TTL window. This is intended.

## Pods are stuck / leaked

- **Pod in `Error`/`CrashLoopBackOff`** — KARA reaps error-state pods on the next reconcile. If it persists, look at the pod logs and events.
- **Orphaned pods** — pods KARA created that the pool no longer reports as agents are deleted on the next reconcile.
- **Deleting the RunnerPool hangs** — the finalizer is waiting on Azure DevOps to unregister agents. Check the operator logs for the unregistration calls; a dead PAT can block this. As a last resort, remove the finalizer manually (you will leak the agents in the pool):

  ```bash
  kubectl -n ci patch runnerpool <name> --type=json -op= /metadata/finalizers
  ```

## Webhook errors on create/update

**Symptom:** `creating resource: ... the server could not find the requested resource` or a TLS error mentioning the webhook.

- **cert-manager not installed** — the certificate is never issued and the webhook has no `caBundle`. Install cert-manager or disable webhooks.
- **Certificate not ready yet** — right after install, wait for `kubectl -n kara-system get certificate` to be `Ready`, then retry.
- **Webhook down** — with `failurePolicy: Fail`, a crashlooping operator blocks writes. Check `kubectl -n kara-system logs deploy/kara-controller-manager`.

## Status looks stale

- `status.lastPolled` tells you the last successful poll. If it is old, the reconcile is erroring — see `status.lastError` and the operator logs.
- The status is updated once per reconcile; with `pollIntervalSeconds: 5` it is at most ~5 s stale.
