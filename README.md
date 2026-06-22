# crsync Controller

## Debugging the Controller Locally

This guide explains how to run and debug the crsync controller on your local machine using [kind](https://kind.sigs.k8s.io/) clusters.

The controller uses the kind multicluster provider, which discovers spoke clusters directly via Docker — no in-cluster networking setup is required.

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
