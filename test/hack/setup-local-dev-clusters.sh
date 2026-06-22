#!/usr/bin/env bash

# Sets up kind clusters for running the crsync controller locally.
# Creates a hub cluster and N spoke clusters. No in-cluster networking setup
# is needed because the controller runs on the developer's machine and the
# kind provider discovers spokes directly via Docker.
#
# Use cleanup-kind-clusters.sh to tear down the clusters afterwards.

set -euo pipefail

WORKSPACE=$(git rev-parse --show-toplevel)
SCRIPT_DIR="${WORKSPACE}/test/hack"

HUB_CLUSTER="crsynchub"
SPOKE_PREFIX="crsyncspoke"
NUM_SPOKES=1
CLEANUP_FIRST=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --hub)
            HUB_CLUSTER="$2"
            shift 2
        ;;
        --spokes)
            NUM_SPOKES="$2"
            shift 2
        ;;
        --spoke-prefix)
            SPOKE_PREFIX="$2"
            shift 2
        ;;
        --cleanup)
            CLEANUP_FIRST=true
            shift
        ;;
        *)
            echo "Usage: $0 [--hub NAME (default: ${HUB_CLUSTER})] [--spokes N (default: ${NUM_SPOKES})] [--spoke-prefix PREFIX (default: ${SPOKE_PREFIX})] [--cleanup]"
            exit 1
        ;;
    esac
done

# ---------------------------------------------------------------------------
# Optional cleanup of existing clusters before (re-)creating them.
# ---------------------------------------------------------------------------
if [[ "${CLEANUP_FIRST}" == "true" ]]; then
    echo "Running cleanup before creating clusters..."
    bash "${SCRIPT_DIR}/cleanup-kind-clusters.sh" --hub "${HUB_CLUSTER}" --spokes "${NUM_SPOKES}" --spoke-prefix "${SPOKE_PREFIX}"
fi

# ---------------------------------------------------------------------------
# Collect all cluster names we intend to create.
# ---------------------------------------------------------------------------
CLUSTERS=("${HUB_CLUSTER}")
for i in $(seq 1 "${NUM_SPOKES}"); do
    CLUSTERS+=("${SPOKE_PREFIX}${i}")
done

# ---------------------------------------------------------------------------
# Pre-flight: check that none of the clusters already exist.
# ---------------------------------------------------------------------------
EXISTING_CLUSTERS=$(kind -- get clusters 2> /dev/null || true)
CONFLICTS=()
for name in "${CLUSTERS[@]}"; do
    if echo "${EXISTING_CLUSTERS}" | grep -qx "${name}"; then
        CONFLICTS+=("${name}")
    fi
done

SKIP_CREATE=false
if [[ ${#CONFLICTS[@]} -eq ${#CLUSTERS[@]} ]]; then
    echo "All clusters already exist, skipping creation."
    SKIP_CREATE=true
    elif [[ ${#CONFLICTS[@]} -gt 0 ]]; then
    echo "ERROR: The following kind clusters already exist: ${CONFLICTS[*]}"
    if [[ -n "${CRSYNC_CLEANUP_BEFORE_CREATE:-}" ]]; then
        echo "CRSYNC_CLEANUP_BEFORE_CREATE is set - running cleanup script first..."
        bash "${SCRIPT_DIR}/cleanup-kind-clusters.sh" --hub "${HUB_CLUSTER}" --spokes "${NUM_SPOKES}" --spoke-prefix "${SPOKE_PREFIX}"
    else
        echo "Set CRSYNC_CLEANUP_BEFORE_CREATE=1 to automatically clean up before creating."
        exit 1
    fi
fi

# ---------------------------------------------------------------------------
# Create all clusters.
# ---------------------------------------------------------------------------
if [[ "${SKIP_CREATE}" == "false" ]]; then
    echo "Creating hub cluster: ${HUB_CLUSTER}"
    kind -- create cluster --name "${HUB_CLUSTER}" --wait 60s

    for i in $(seq 1 "${NUM_SPOKES}"); do
        spoke="${SPOKE_PREFIX}${i}"
        echo "Creating spoke cluster: ${spoke}"
        kind -- create cluster --name "${spoke}" --wait 60s
    done
fi

# ---------------------------------------------------------------------------
# Apply bootstrapping manifests (e.g. test CRDs) to every cluster.
# ---------------------------------------------------------------------------
MANIFESTS_DIR="${SCRIPT_DIR}/manifests"
if [[ -d "${MANIFESTS_DIR}" ]]; then
    for name in "${CLUSTERS[@]}"; do
        echo "Applying bootstrap manifests to cluster: ${name}"
        kubectl apply --context "kind-${name}" -f "${MANIFESTS_DIR}/"
    done
fi

# ---------------------------------------------------------------------------
# Export the hub kubeconfig to a dedicated temp file.
# ---------------------------------------------------------------------------
KUBECONFIG_PATH="/tmp/kubeconfig-${HUB_CLUSTER}"
kind -- get kubeconfig --name "${HUB_CLUSTER}" > "${KUBECONFIG_PATH}"
chmod 600 "${KUBECONFIG_PATH}"
echo "Hub kubeconfig written to: ${KUBECONFIG_PATH}"

# ---------------------------------------------------------------------------
# Print a summary and the controller flags needed for local development.
# ---------------------------------------------------------------------------

echo ""
echo "Setup complete."
echo "  Hub cluster:   ${HUB_CLUSTER} (kubectl context: kind-${HUB_CLUSTER})"
for i in $(seq 1 "${NUM_SPOKES}"); do
    echo "  Spoke cluster: ${SPOKE_PREFIX}${i} (kubectl context: kind-${SPOKE_PREFIX}${i})"
done
echo ""
echo "Run the controller with:"
echo "  --use-kind-provider \\"
echo "  --kind-hub-kubeconfig ${KUBECONFIG_PATH} \\"
echo "  --kind-clusters-prefix ${SPOKE_PREFIX}"
