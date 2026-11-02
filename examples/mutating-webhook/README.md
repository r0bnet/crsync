# Inject data from the workload cluster with a mutating webhook

A MutatingAdmissionPolicy sees only the object and its hub namespace ([example](../mutating-admission-policy-prefix)). A webhook can read anything, including the workload cluster the object came from.

`main.go` handles hub copies of the imaginary `Queue`:

1. It reads crsync's source labels from the hub namespace: the source cluster and the source namespace.
2. It gets the cluster's ClusterProfile and builds a client through its access provider, the same way crsync does.
3. It reads the source namespace on the workload cluster and copies its `example.io/cost-center` label to `spec.costCenter`.

```bash
examples/mutating-webhook/install.sh
kubectl --context kind-crsyncspoke1 label namespace default example.io/cost-center=cc-42
kubectl --context kind-crsyncspoke1 apply -f examples/mutating-webhook/queue.yaml
kubectl --context kind-crsynchub -n fleet-crsyncspoke1--default get queue orders -o jsonpath='{.spec.costCenter}'
```

`install.sh` builds the image, creates a self-signed certificate, deploys `deploy.yaml` and adds `Queue` to crsync's resources. The webhook runs on every write by crsync, so a changed label reaches the hub with the next change to the `Queue`, not right away.
