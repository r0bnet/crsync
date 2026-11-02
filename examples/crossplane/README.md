# Crossplane: one composition, per-cluster credentials

Workload clusters get only the XR's CRD. crsync mirrors each `XDatabase` into the hub namespace of its cluster (`fleet-<cluster>--<namespace>`), where Crossplane composes it.

## How the ProviderConfig is selected

Nothing is injected and nothing is copied:

1. Every hub copy carries the label `crsync.r0bnet.github.io/source-cluster` (for example `fleet.crsyncspoke1`). crsync sets it after copying the user's labels, so users can't override it.
2. When a cluster is onboarded, the central controller (here: `install.sh`) creates a `ClusterProviderConfig` with exactly that name.
3. One composition patches the label into `spec.providerConfigRef.name` (with `kind: ClusterProviderConfig`) of every composed resource.

The label key is a contract between crsync and your compositions. `internal/controller/contract_test.go` fails if it changes.

**Crossplane v1 claims:** the same idea works with a patch from the XR's `crossplane.io/claim-namespace` label, because the claim's namespace is the crsync hub namespace.

## Hub-owned fields

crsync applies only what it got from the workload cluster (field manager `crsync`), so fields Crossplane sets, like `spec.crossplane.compositionRef`, survive every resync.

Users must not set them, or they could pick another Composition and its credentials:

- `install.sh` ships the XR CRD to workload clusters without `spec.crossplane` and `status.crossplane`, so their API server drops those fields. In a real setup the cluster manager (e.g. Sveltos) does this.
- Workload cluster admins can install the full CRD, so `hub-policy.yaml` also reverts any change crsync makes to `spec.crossplane` or `crossplane.io/*` annotations on hub copies.

## Events on the workload cluster

With `eventReplay.enabled=true`, users see hub Events, including those of composed resources, in `kubectl describe xdatabase orders`. To scope the owner walk's RBAC:

```bash
helm upgrade crsync charts/crsync --reuse-values \
  --set eventReplay.enabled=true \
  --set-json 'ownerResources=[{"group":"kubernetes.m.crossplane.io","resources":["objects"]}]'
```

### Example: a mistake the user can fix

`team` is free text in the XRD, but the composition turns it into a label, so `team: "Payments Team"` fails on the hub:

```bash
kubectl --context kind-crsyncspoke1 apply -f examples/crossplane/xdatabase-invalid-team.yaml
kubectl --context kind-crsyncspoke1 describe xdatabase payments
#   Synced=False  ReconcileError  Unsynced resources: database
#   Warning  ComposeResources  cannot apply composed resource "database": ... Invalid value: "Payments Team": a valid label must ...
kubectl --context kind-crsyncspoke1 patch xdatabase payments --type=merge -p '{"spec":{"team":"payments-team"}}'
#   Ready=True, plus "Object: Successfully requested creation ..." from the provider
```

Messages like this one can contain hub names, which crsync can't scrub.

## Connection Secrets on the workload cluster

The `Object` writes a connection Secret, `<name>-connection`, owned by the `Object` and thus by the XR. `install.sh` enables write-back for Secrets, so it appears next to the user's `XDatabase` and goes away with it:

```bash
kubectl --context kind-crsyncspoke1 get secret orders-connection -o jsonpath='{.data.endpoint}' | base64 -d
#   orders.db.example.internal:5432
```

Secrets written without ownerReferences can opt in with `crsync.r0bnet.github.io/write-back: "true"`.

## Run it

```bash
test/hack/setup-e2e-environment.sh
examples/crossplane/install.sh
kubectl --context kind-crsyncspoke1 apply -f examples/crossplane/xdatabase.yaml
kubectl --context kind-crsyncspoke1 get xdatabase orders -o jsonpath='{.status.providerConfig}'   # fleet.crsyncspoke1
```

Pinned versions: Crossplane 2.4.3, function-patch-and-transform v0.11.2, provider-kubernetes v1.4.1.
The demo's `ClusterProviderConfig`s all use the hub's in-cluster identity. In a real setup each would hold one cloud identity per cluster.
