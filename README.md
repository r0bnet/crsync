# crsync

crsync syncs custom resources from many workload clusters (spokes) into one central cluster (hub), and copies their status back. Users keep using `kubectl` or GitOps against the CRDs on their own cluster. The controller that does the actual work (for example Crossplane) runs only once, on the hub, where users have no access.

crsync runs only on the hub; nothing extra is installed on the spokes. It is built on [multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime).

## How it works

```
spoke (fleet/spoke1)                      hub
default/orders  ── spec, labels ──▶  fleet-spoke1--default/orders ──▶ hub controller (e.g. Crossplane)
                ◀── status ─────────
                ◀── Secrets in the hub namespace
                ◀── Events (opt-in)
```

- **Discovery:** spokes are [ClusterProfiles](https://github.com/kubernetes-sigs/cluster-inventory-api) on the hub. crsync connects to a cluster once its profile has `ControlPlaneHealthy=True`, and gets credentials through the profile's `accessProviders` and an exec plugin (`kubeconfig-secretreader` in the chart). Deleting the profile disconnects the cluster and deletes its hub namespaces.
- **Hub namespaces:** each spoke namespace gets one hub namespace, `<cluster>--<namespace>`. A cluster `fleet/spoke1` becomes `fleet-spoke1--default`. crsync refuses a namespace that already belongs to another cluster.
- **Labels (contract):** every hub copy carries `crsync.r0bnet.github.io/source-cluster` (e.g. `fleet.spoke1`), `source-namespace` and `source-name`. crsync sets them after copying the user's labels, so users can't override them. Compositions and policies may select on them; [examples/crossplane](examples/crossplane) uses the cluster label to pick a per-cluster `ClusterProviderConfig`.
- **Spec (spoke → hub):** crsync writes hub copies with server-side apply (field manager `crsync`) and only sends what it got from the spoke: `spec`, labels, annotations. Fields that only hub controllers set (e.g. Crossplane's `spec.crossplane.compositionRef`) survive every resync. If a hub controller changed a field the spoke also sets, crsync records a `FieldConflict` Warning Event on the hub copy and the spoke's value wins.
- **Status (hub → spoke):** status changes on the hub copy are copied to the spoke object.
- **Deletion:** a finalizer on the spoke object keeps it until the hub copy is gone, including any finalizers hub controllers put on it.
- **Secrets (hub → spoke):** Secrets in crsync's hub namespaces (e.g. connection details) are copied into the spoke namespace, optionally filtered by type.
- **Events (hub → spoke, opt-in):** see [Event replay](#event-replay).

Anything crsync shouldn't know about, such as cluster-specific changes to hub copies, belongs on the hub. [examples/mutating-admission-policy](examples/mutating-admission-policy) shows a `MutatingAdmissionPolicy` for that.

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
resources:            # GVKs to sync; without a version, all versions served by the hub are used
  - group: demo.crsync.io
    kind: XDatabase
secretTypes:          # optional; Secret types copied from hub to spoke (empty = all)
  - Opaque
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
| `config.secretTypes` | `[]` | See above |
| `clusterProfile.accessProvider.image` | `kubeconfig-secretreader:v0.1.3` | Exec plugin mounted as an image volume (needs Kubernetes image volumes) |
| `eventReplay.enabled` | `false` | Event replay plus its RBAC |
| `eventReplay.ownerResources` | `[]` | Types readable during the owner walk; empty means `get` on `*/*` |

crsync talks to spokes with the credentials from their kubeconfig Secrets. Those need read/write access to the synced resources and Secrets, and `create`/`patch` on `events.k8s.io` Events if event replay is on.

## Event replay

With `--replay-events`, Events on the hub are copied onto the spoke object, so users can see why their resource isn't ready without access to the hub.

- Events on the hub copy itself are copied as they are.
- Events on resources created *from* the hub copy (e.g. Crossplane's composed resources) are attributed by walking `controller` ownerReferences up to the hub copy: at most 5 hops, within the namespace. The copied note names only the resource's kind (`Object: ...`), never its hub name. Provider messages themselves can still contain hub details.
- If any object in the chain can't be read, or has no controlling owner, the Event is skipped.
- crsync's own Events (e.g. `FieldConflict`) are not copied.
- Updates to an Event (count, last seen) update the same spoke Event.

It's off by default because the owner walk needs `get` on every type in the chain. That's `*/*` unless you list them in `eventReplay.ownerResources`. When enabled, crsync also watches all Events on the hub.

## Development

```bash
go test ./...   # unit tests
go vet ./...
```

`test/hack/setup-e2e-environment.sh` is the end-to-end setup: it builds the image from `test/hack/Dockerfile`, registers the spokes as ClusterProfiles in the `fleet` namespace (`PROFILE_NAMESPACE`), and installs the chart.

## Debugging the Controller Locally

This guide explains how to run and debug the crsync controller on your local machine using [kind](https://kind.sigs.k8s.io/) clusters.

For local debugging the controller uses the kind multicluster provider (`--use-kind-provider`), which discovers spoke clusters directly via Docker — no in-cluster networking, ClusterProfiles or access plugin are needed.

### Prerequisites

- [`kind`](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- [`kubectl`](https://kubernetes.io/docs/tasks/tools/)
- A [VS Code Go debugger](https://marketplace.visualstudio.com/items?itemName=golang.go) setup

### How it works

The `test/hack/setup-local-dev-clusters.sh` script creates:

- one **hub** kind cluster (default name: `crsynchub`)
- one or more **spoke** kind clusters (default prefix: `crsyncspoke`, so `crsyncspoke1`, `crsyncspoke2`, …)

It also exports the hub kubeconfig to a fixed path (`/tmp/kubeconfig-<hub-name>`) so the VS Code launch configuration can reference it without knowing a randomly-generated filename.

The script is **idempotent**: if all clusters already exist it skips creation and only refreshes the kubeconfig file. This makes it safe to use as a VS Code pre-task that runs on every debug session.

### Creating the local controller config

The controller requires a config file that tells it which resources to sync. For local development, create `config/local-dev.yaml` based on the provided sample:

```bash
cp config/samples/config.yaml \
   config/local-dev.yaml
```

Then edit the file to include the resource GVKs you want to sync during your session. For example:

```yaml
# List of GVKs the controller will watch across all clusters.
resources:
  - group: ""
    version: "v1"
    kind: "ConfigMap"
```

`config/local-dev.yaml` is listed in `config/.gitignore` and will not be committed.

### Setting up the VS Code launch target

Create or extend `.vscode/launch.json` in the workspace root with an entry for the controller. The `preLaunchTask` runs the cluster setup script before each debug session:

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

Add the corresponding task to `.vscode/tasks.json`:

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

When you start the `crsync: debug (kind)` launch target, VS Code runs the setup script first. Because the script is idempotent, it completes quickly if the clusters are already running.

### Starting fresh: cleaning up existing clusters

To delete all local dev clusters and start from scratch, run the cleanup script manually:

```bash
./test/hack/cleanup-kind-clusters.sh
```

Pass the same `--hub` and `--spoke-prefix` values you used during setup if you deviated from the defaults:

```bash
./test/hack/cleanup-kind-clusters.sh \
  --hub crsynchub \
  --spokes 2 \
  --spoke-prefix crsyncspoke
```

Alternatively, pass `--cleanup` to the setup script itself to tear down and recreate all clusters in one step:

```bash
./test/hack/setup-local-dev-clusters.sh --cleanup
```

This is useful when you want to wipe cluster state but keep the debug workflow running. You can also wire it as a separate VS Code task that you invoke manually when needed.
