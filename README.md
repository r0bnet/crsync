# crsync

crsync syncs custom resources from many workload clusters (spokes) into one central cluster (hub), and copies their status back. Users keep using `kubectl` or GitOps against the CRDs on their own cluster. The controller that does the actual work (for example Crossplane) runs only once, on the hub, where users have no access.

crsync runs only on the hub; nothing extra is installed on the spokes. It is built on [multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime).

## How it works

```
spoke (fleet/spoke1)                      hub
default/orders  ── spec, labels ──▶  fleet-spoke1--default/orders ──▶ hub controller (e.g. Crossplane)
                ◀── status ─────────
                ◀── objects owned by the hub copy (write-back, opt-in)
                ◀── Events (opt-in)
```

- **Discovery:** spokes are [ClusterProfiles](https://github.com/kubernetes-sigs/cluster-inventory-api) with `ControlPlaneHealthy=True`; credentials come from the profile's `accessProviders` via an exec plugin. Deleting a profile deletes its hub namespaces; restarts don't.
- **Hub namespaces:** `<cluster>--<namespace>`, e.g. `fleet-spoke1--default` for cluster `fleet/spoke1`.
- **Labels (contract):** hub copies carry `crsync.r0bnet.github.io/source-cluster` (e.g. `fleet.spoke1`), `source-namespace` and `source-name`. Users can't override them; compositions may select on them.
- **Spec (spoke → hub):** server-side apply with field manager `crsync`, sending only `spec`, labels and annotations. Hub-only fields survive; on a conflict the spoke wins and crsync records a `FieldConflict` Event on the hub copy.
- **Status (hub → spoke)** is copied back. A finalizer keeps the spoke object until the hub copy is gone.
- **Opt-in:** [write-back](#write-back) and [event replay](#event-replay).

Cluster-specific changes to hub copies belong on the hub, e.g. a [MutatingAdmissionPolicy](examples/mutating-admission-policy).

## Quick start on kind

Needs `kind`, `kubectl`, `helm`, `yq` and Docker.

```bash
test/hack/setup-e2e-environment.sh   # hub + 1 spoke, ClusterProfile per spoke, crsync via Helm
kubectl --context kind-crsyncspoke1 apply -f config/samples/crsynctest-cr.yaml
kubectl --context kind-crsynchub get crsynctests -n fleet-crsyncspoke1--default
```

Then try the Crossplane example: [examples/crossplane](examples/crossplane). Clean up with `test/hack/cleanup-kind-clusters.sh`.

## Configuration

### Synced resources (`--config`)

```yaml
resources:            # GVKs to sync; without a version, the hub's preferred version is used
  - group: demo.crsync.io
    kind: XDatabase
writeBack:            # optional; GVKs copied back to the spoke, see Write-back
  - group: ""
    version: v1
    kind: Secret
```

The hub and every spoke need the CRDs of these resources. Spokes need only the CRDs, not the controllers behind them.

### Flags

| Flag | Default | Purpose |
|------|---------|---------|
| `--config` | `$CONFIG_FILE` | The config file above |
| `--clusterprofile-provider-file` | | Access provider config for the ClusterProfile provider (JSON). Required unless `--use-kind-provider` |
| `--replay-events` | `false` | Enable [event replay](#event-replay) |
| `--hub-max-concurrent-reconciles` / `--spoke-max-concurrent-reconciles` | `1` | Concurrency per controller |
| `--leader-elect` | `false` | Leader election |
| `--use-kind-provider`, `--kind-hub-kubeconfig`, `--kind-clusters-prefix` | | Local debugging only, see below |

### Helm chart (`charts/crsync`)

| Value | Default | Purpose |
|-------|---------|---------|
| `config.resources` | `[]` | Synced GVKs. Each entry also needs `resource` (plural), used for RBAC |
| `config.writeBack` | `[]` | [Write-back](#write-back) GVKs. Each entry also needs `resource` (plural), used for RBAC |
| `config.writeBackCopyLabel` | `""` | Label key marking write-back copies; empty uses the default |
| `clusterProfile.accessProvider.image` | `kubeconfig-secretreader:v0.1.3` | Exec plugin mounted as an image volume (needs Kubernetes image volumes) |
| `eventReplay.enabled` | `false` | Event replay plus its RBAC |
| `ownerResources` | `[]` | Types readable during the owner walk (event replay, write-back); empty means `get` on `*/*` |

crsync talks to spokes with the credentials from their kubeconfig Secrets. Those need read/write access to the synced resources and the write-back GVKs, and `create`/`patch` on `events.k8s.io` Events if event replay or write-back is on.

## Event replay

`--replay-events` copies hub Events onto the spoke object, so users see why their resource isn't ready.

- Events on resources created from the hub copy (e.g. composed resources) are found by walking `controller` ownerReferences (max 5 hops). The note names only the kind (`Object: ...`), not the hub name.
- Events whose owner chain can't be read are skipped, and so are crsync's own.

Off by default: the walk needs `get` on every owner type, which is `*/*` unless you list them in `ownerResources`.

## Write-back

`writeBack` lists hub GVKs, e.g. Secrets, to copy into the source namespace on the spoke. An object is copied if its owner chain leads to a hub copy, or if it has `crsync.r0bnet.github.io/write-back: "true"` (for objects without owners).

- The copy keeps the name and everything but `metadata` and `status`, and gets the label `crsync.r0bnet.github.io/write-back-copy: "true"` (key configurable with `writeBackCopyLabel`; changing it orphans existing copies). crsync never touches objects without that label (`WriteBackConflict` Warning).
- Owned copies get an ownerReference to the spoke object. All copies go when their hub object goes.
- A GVK the spoke doesn't serve gives a `WriteBackTypeMissing` Warning and is retried. A GVK can't be in both `resources` and `writeBack`.

## Development

```bash
go test ./...   # unit tests
go vet ./...
```

`test/hack/setup-e2e-environment.sh` is the end-to-end setup: it builds the image from `test/hack/Dockerfile`, registers the spokes as ClusterProfiles in the `fleet` namespace (`PROFILE_NAMESPACE`), and installs the chart.

## Debugging locally

`--use-kind-provider` discovers spokes as kind clusters through Docker, without ClusterProfiles or the access plugin. Needs `kind`, `kubectl` and the VS Code Go extension.

`test/hack/setup-local-dev-clusters.sh` creates a hub (`crsynchub`) and spokes (`crsyncspoke1`, …) if missing, and writes the hub kubeconfig to `/tmp/kubeconfig-crsynchub`. It's idempotent, so it works as a pre-launch task. Copy `config/samples/config.yaml` to `config/local-dev.yaml` (gitignored) and list the GVKs to sync.

`.vscode/launch.json`:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "crsync: debug (kind)",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}/cmd",
      "preLaunchTask": "crsync: setup local dev clusters",
      "args": [
        "--config", "${workspaceFolder}/config/local-dev.yaml",
        "--use-kind-provider",
        "--kind-hub-kubeconfig", "/tmp/kubeconfig-crsynchub",
        "--kind-clusters-prefix", "crsyncspoke"
      ]
    }
  ]
}
```

`.vscode/tasks.json`:

```json
{
  "version": "2.0.0",
  "tasks": [
    {
      "label": "crsync: setup local dev clusters",
      "type": "shell",
      "command": "${workspaceFolder}/test/hack/setup-local-dev-clusters.sh --spokes ${input:crsyncNumSpokes} ${input:crsyncCleanup}",
      "presentation": {
        "reveal": "always",
        "panel": "dedicated"
      }
    }
  ],
  "inputs": [
    {
      "id": "crsyncNumSpokes",
      "type": "promptString",
      "description": "Number of spoke clusters to create",
      "default": "1"
    },
    {
      "id": "crsyncCleanup",
      "type": "pickString",
      "description": "Wipe existing clusters before creating new ones?",
      "options": [
        {
          "label": "No (keep existing clusters)",
          "value": ""
        },
        {
          "label": "Yes (wipe and recreate)",
          "value": "--cleanup"
        }
      ],
      "default": ""
    }
  ]
}
```

Clean up with `test/hack/cleanup-kind-clusters.sh` (pass `--hub`, `--spokes`, `--spoke-prefix` if you changed them), or recreate everything with `setup-local-dev-clusters.sh --cleanup`.
