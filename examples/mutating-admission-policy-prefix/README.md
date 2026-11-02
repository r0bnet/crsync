# Prefix a spec field with the source cluster

A MutatingAdmissionPolicy can read the object and its namespace (`namespaceObject`), but no other objects. crsync labels every hub namespace with the source cluster, so a policy can use it.

`policy.yaml` sets `spec.bucketName` of the imaginary `Bucket` to `<source cluster>-<spec.name>`, e.g. `fleet-crsyncspoke1-logs`. Buckets with the same name on different workload clusters get distinct names on the hub.

```bash
kubectl --context kind-crsynchub apply -f examples/mutating-admission-policy-prefix/crd.yaml -f examples/mutating-admission-policy-prefix/policy.yaml
kubectl --context kind-crsyncspoke1 apply -f examples/mutating-admission-policy-prefix/crd.yaml -f examples/mutating-admission-policy-prefix/bucket.yaml
```

crsync must sync `buckets.storage.example.io` (add it to `config.resources`). The source namespace on the workload cluster isn't visible to the policy, only the hub namespace and its labels.

This is limited, but easy to set up. Anything more needs a policy engine like Kyverno or a custom mutating webhook. These can read other resources on the hub, and a webhook can even read the source cluster: it looks up the ClusterProfile and connects through its access provider.
