# Generic transformations with a MutatingAdmissionPolicy

crsync copies resources unchanged. If hub copies need cluster-specific changes, mutate them on admission in the hub instead of teaching crsync a transformation language.

`policy.yaml` adds the annotation `crsync.example/source-cluster` to every `CRSyncTest` in a hub namespace created by crsync. It selects on crsync's `crsync.r0bnet.github.io/source-cluster` namespace label and reads the value from the object's own label.

```bash
kubectl --context kind-crsynchub apply -f examples/mutating-admission-policy/policy.yaml
```

This needs Kubernetes 1.36+ (`admissionregistration.k8s.io/v1`, enabled by default).
Fields set this way are recorded under crsync's field manager. The policy runs on every write, so they stay in place.
