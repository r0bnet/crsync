#!/usr/bin/env bash

# Sets up the full e2e test environment for the crsync controller.
#
# 1. Creates kind clusters (hub + spokes) with in-cluster networking via
#    setup-kind-clusters.sh.
# 2. Applies the test CRD to ALL clusters so API discovery works even when
#    the controller config omits the version field.
# 3. Builds the controller image via docker, loads it into the hub cluster,
#    and deploys the Helm chart.
#
# Use cleanup-kind-clusters.sh to tear down the clusters afterwards.

set -euo pipefail

WORKSPACE=$(git rev-parse --show-toplevel)
SCRIPT_DIR="${WORKSPACE}/test/hack"
CHART_DIR="${WORKSPACE}/charts/crsync"
MANIFESTS_DIR="${SCRIPT_DIR}/manifests"

HUB_CLUSTER="crsynchub"
SPOKE_CLUSTERS_PREFIX="crsyncspoke"
NUM_SPOKES=1
RELEASE_NAMESPACE="crsync-system"
CONTROLLER_IMAGE="r0bnet/crsync:e2e"

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
            SPOKE_CLUSTERS_PREFIX="$2"
            shift 2
        ;;
        --image)
            CONTROLLER_IMAGE="$2"
            shift 2
        ;;
        *)
            echo "Usage: $0 [--hub NAME] [--spokes N] [--spoke-prefix PREFIX] [--image IMAGE:TAG]"
            exit 1
        ;;
    esac
done

# ---------------------------------------------------------------------------
# Step 1: Create kind clusters with in-cluster networking and register the
#         spokes as ClusterProfiles on the hub.
# ---------------------------------------------------------------------------
echo "=== Step 1: Creating kind clusters ==="
bash "${SCRIPT_DIR}/setup-kind-clusters.sh" \
--hub "${HUB_CLUSTER}" \
--spokes "${NUM_SPOKES}" \
--spoke-prefix "${SPOKE_CLUSTERS_PREFIX}"

# ---------------------------------------------------------------------------
# Step 2: Apply the test CRD to ALL clusters (hub + spokes) so that API
#         discovery resolves the GVK even without an explicit version in
#         the controller config.
# ---------------------------------------------------------------------------
echo ""
echo "=== Step 2: Applying test CRD to all clusters ==="

CLUSTERS=("${HUB_CLUSTER}")
for i in $(seq 1 "${NUM_SPOKES}"); do
    CLUSTERS+=("${SPOKE_CLUSTERS_PREFIX}${i}")
done

for name in "${CLUSTERS[@]}"; do
    echo "Applying test CRD to cluster: ${name}"
    kubectl apply --context "kind-${name}" -f "${MANIFESTS_DIR}/crsynctest-crd.yaml"
done

# ---------------------------------------------------------------------------
# Step 3: Build the controller image, load it into the hub cluster, and
#         deploy the Helm chart.
# ---------------------------------------------------------------------------
echo ""
echo "=== Step 3: Building and loading controller image ==="

# Build the image and load it into the local Docker daemon without starting
# a container.
docker build -t "${CONTROLLER_IMAGE}" -f "${SCRIPT_DIR}/Dockerfile" "${WORKSPACE}"

# Load into the hub kind cluster.
kind load docker-image "${CONTROLLER_IMAGE}" --name "${HUB_CLUSTER}"

# ---------------------------------------------------------------------------
# Step 4: Deploy the Helm chart to the hub cluster, using the loaded image and
#         pointing to the test CRD GVR.
# ---------------------------------------------------------------------------
echo ""
echo "=== Step 4: Deploying crsync Helm chart to hub cluster ==="

kubectl --context "kind-${HUB_CLUSTER}" create namespace "${RELEASE_NAMESPACE}" 2>/dev/null || true

# Split CONTROLLER_IMAGE into registry and tag for Helm values.
IMAGE_REGISTRY="${CONTROLLER_IMAGE%:*}"
IMAGE_TAG="${CONTROLLER_IMAGE##*:}"

helm upgrade --install crsync "${CHART_DIR}" \
--kube-context "kind-${HUB_CLUSTER}" \
--namespace "${RELEASE_NAMESPACE}" \
--set "image.registry=${IMAGE_REGISTRY}" \
--set "image.tag=${IMAGE_TAG}" \
--set "image.pullPolicy=Never" \
--set-json 'config.resources=[{"group":"crsync.test.io","kind":"CRSyncTest","resource":"crsynctests"}]' \
--wait --timeout 120s

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== E2E environment ready ==="
echo "  Hub cluster : kind-${HUB_CLUSTER} (namespace: ${RELEASE_NAMESPACE})"
for i in $(seq 1 "${NUM_SPOKES}"); do
    echo "  Spoke cluster: kind-${SPOKE_CLUSTERS_PREFIX}${i}"
done
echo "  Controller image: ${CONTROLLER_IMAGE}"
echo "  Test CRD: crsync.test.io/CRSyncTest (version discovered via API)"
