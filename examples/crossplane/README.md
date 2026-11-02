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

crsync writes hub copies with server-side apply (field manager `crsync`) and only sends what it got from the workload cluster. Crossplane's `spec.crossplane.compositionRef` and similar fields stay owned by Crossplane and survive every resync.

## Events on the workload cluster

With `eventReplay.enabled=true`, crsync copies hub Events onto the user's `XDatabase` on the workload cluster. That includes Events on composed resources: crsync walks `ownerReferences` from the composed `Object` up to the hub copy. Users see them in `kubectl describe xdatabase orders`. Copied notes name only the composed resource's kind (`Object: ...`), never its hub name. crsync's own Events (for example `FieldConflict`) aren't copied, and Events whose owner chain can't be read are skipped.

The walk needs `get` on every type in the chain. By default the chart grants `get` on `*/*`. Scope it with:

```bash
helm upgrade crsync charts/crsync --reuse-values \
  --set eventReplay.enabled=true \
  --set-json 'eventReplay.ownerResources=[{"group":"kubernetes.m.crossplane.io","resources":["objects"]}]'
```

## Run it

```bash
test/hack/setup-e2e-environment.sh
examples/crossplane/install.sh
kubectl --context kind-crsyncspoke1 apply -f examples/crossplane/xdatabase.yaml
kubectl --context kind-crsyncspoke1 get xdatabase orders -o jsonpath='{.status.providerConfig}'   # fleet.crsyncspoke1
```

Pinned versions: Crossplane 2.4.3, function-patch-and-transform v0.11.2, provider-kubernetes v1.4.1.
The demo's `ClusterProviderConfig`s all use the hub's in-cluster identity. In a real setup each would hold one cloud identity per cluster.
