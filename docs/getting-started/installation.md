# Installation

KARA ships two equivalent installation paths: a Kustomize bundle (or the rendered `dist/install.yaml`) and a Helm chart. Both install the same things:

- The `runnerpools.kara.felukka.org` CRD
- The operator Deployment (metrics, health probes, webhooks)
- Service accounts and RBAC (manager, leader election, metrics auth)
- The metrics and webhook Services
- cert-manager Issuer + Certificate and the mutating/validating webhook configurations (when enabled)

## Helm

```bash
helm install kara chart/kara \
  --namespace kara-system --create-namespace \
  --set image.repository=<some-registry>/kara \
  --set image.tag=tag
```

Or from a packaged chart:

```bash
make dist
helm install kara dist/kara-<version>.tgz \
  --namespace kara-system --create-namespace \
  --set image.repository=<some-registry>/kara \
  --set image.tag=tag
```

The useful settings are documented in [Helm chart](../helm/kara.md).

> The default `image.repository` in the chart is `ghcr.io/mahmoudk1000/kara/operator`. Set `image.repository` and `image.tag` to wherever you built and pushed the operator image.

## Kustomize

```bash
# build the operator image first
make docker-build docker-push IMG=<some-registry>/kara:tag

# install the CRDs
make install

# deploy the operator
make deploy IMG=<some-registry>/kara:tag
```

Or apply the full bundle:

```bash
kubectl apply -k config/default
```

## Verify

```bash
kubectl -n kara-system get deployment
kubectl -n kara-system get svc
kubectl get crd runnerpools.kara.felukka.org
```

Wait for the Deployment to become ready:

```bash
kubectl -n kara-system rollout status deployment/kara-controller-manager
```

If the webhooks are enabled, cert-manager should also report the certificate as ready:

```bash
kubectl -n kara-system get certificate
```

## Uninstall

**Helm:**

```bash
helm delete kara -n kara-system
```

**Kustomize:**

```bash
make undeploy
make uninstall
```

> !!! warning
> Deleting the operator does **not** delete your `RunnerPool` resources or the agents they manage. Delete the RunnerPools first (KARA's finalizer unregisters the agents and cleans up pods/PVCs), then delete the operator.
