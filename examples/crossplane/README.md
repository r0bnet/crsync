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
  --set-json 'ownerResources=[{"group":"kubernetes.m.crossplane.io","resources":["objects"]}]'
```

### Example: a mistake the user can fix

`team` is free text in the XRD, so `xdatabase-invalid-team.yaml` (`team: "Payments Team"`) is accepted on the workload cluster. On the hub the composition turns it into a label, and Kubernetes rejects the space:

```bash
kubectl --context kind-crsyncspoke1 apply -f examples/crossplane/xdatabase-invalid-team.yaml
kubectl --context kind-crsyncspoke1 describe xdatabase payments
#   Synced=False  ReconcileError  Unsynced resources: database
#   Warning  ComposeResources  cannot apply composed resource "database": ... Invalid value: "Payments Team": a valid label must ...
kubectl --context kind-crsyncspoke1 patch xdatabase payments --type=merge -p '{"spec":{"team":"payments-team"}}'
#   Ready=True, plus "Object: Successfully requested creation ..." from the provider
```

The Warning comes from Crossplane composing on the XR itself, because provider-kubernetes's `Object` CRD already validates the embedded labels. The provider's own Events reach the user through the ownerReference walk. Messages like this one contain hub names (here the composed `Object`'s name), which crsync can't scrub.

## Connection Secrets on the workload cluster

The composition makes the `Object` write a connection Secret, `<name>-connection`, with the database `endpoint`. Crossplane owns it through the `Object`, which is owned by the XR, so crsync's write-back finds the hub copy by owner walk. `install.sh` enables it with `config.writeBack` for Secrets:

```bash
kubectl --context kind-crsyncspoke1 get secret orders-connection -o jsonpath='{.data.endpoint}' | base64 -d
#   orders.db.example.internal:5432
```

Deleting the `XDatabase` deletes the Secret. A hub controller that writes Secrets without ownerReferences can opt them in with the annotation `crsync.r0bnet.github.io/write-back: "true"` instead.

## Run it

```bash
test/hack/setup-e2e-environment.sh
examples/crossplane/install.sh
kubectl --context kind-crsyncspoke1 apply -f examples/crossplane/xdatabase.yaml
kubectl --context kind-crsyncspoke1 get xdatabase orders -o jsonpath='{.status.providerConfig}'   # fleet.crsyncspoke1
```

Pinned versions: Crossplane 2.4.3, function-patch-and-transform v0.11.2, provider-kubernetes v1.4.1.
The demo's `ClusterProviderConfig`s all use the hub's in-cluster identity. In a real setup each would hold one cloud identity per cluster.
