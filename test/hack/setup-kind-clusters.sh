#!/usr/bin/env bash

set -euo pipefail

WORKSPACE=$(git rev-parse --show-toplevel)
SCRIPT_DIR="${WORKSPACE}/test/hack"

HUB_CLUSTER="crsynchub"
KUBECONFIGS_NAMESPACE="kubeconfigs"
SPOKE_CLUSTERS_PREFIX="crsyncspoke"
NUM_SPOKES=1
API_SERVER_PORT=6443

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
	*)
		echo "Usage: $0 [--hub NAME (default: ${HUB_CLUSTER})] [--spokes N (default: ${NUM_SPOKES})] [--spoke-prefix PREFIX (default: ${SPOKE_CLUSTERS_PREFIX})]"
		exit 1
		;;
	esac
done

# ---------------------------------------------------------------------------
# Collect all cluster names we intend to create.
# ---------------------------------------------------------------------------
CLUSTERS=("${HUB_CLUSTER}")
for i in $(seq 1 "${NUM_SPOKES}"); do
	CLUSTERS+=("${SPOKE_CLUSTERS_PREFIX}${i}")
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

if [[ ${#CONFLICTS[@]} -gt 0 ]]; then
	echo "ERROR: The following kind clusters already exist: ${CONFLICTS[*]}"
	if [[ -n "${CRSYNC_CLEANUP_BEFORE_CREATE:-}" ]]; then
		echo "CRSYNC_CLEANUP_BEFORE_CREATE is set - running cleanup script first..."
		bash "${SCRIPT_DIR}/cleanup-kind-clusters.sh" --hub "${HUB_CLUSTER}" --spokes "${NUM_SPOKES}" --spoke-prefix "${SPOKE_CLUSTERS_PREFIX}"
	else
		echo "Set CRSYNC_CLEANUP_BEFORE_CREATE=1 to automatically clean up before creating."
		exit 1
	fi
fi

# ---------------------------------------------------------------------------
# Create all clusters.
# ---------------------------------------------------------------------------
echo "Creating hub cluster: ${HUB_CLUSTER}"
kind -- create cluster --name "${HUB_CLUSTER}" --wait 60s

for i in $(seq 1 "${NUM_SPOKES}"); do
	spoke="${SPOKE_CLUSTERS_PREFIX}${i}"
	echo "Creating spoke cluster: ${spoke}"
	kind -- create cluster --name "${spoke}" --wait 60s --config <(
		cat << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
kubeadmConfigPatches:
  - |
    kind: ClusterConfiguration
    apiServer:
      certSANs:
        - localhost
        - 127.0.0.1
        - ${spoke}.default.svc.cluster.local
EOF
	)
done

kubectl --context "kind-${HUB_CLUSTER}" create namespace "${KUBECONFIGS_NAMESPACE}" || true

# ---------------------------------------------------------------------------
# For each spoke, expose its control plane to the hub cluster via a
# headless Service + manual Endpoints in the hub's default namespace.
# ---------------------------------------------------------------------------
for i in $(seq 1 "${NUM_SPOKES}"); do
	spoke="${SPOKE_CLUSTERS_PREFIX}${i}"
	container="${spoke}-control-plane"

	echo "Configuring hub access to spoke: ${spoke}"

	# Get the control plane node's IP address from the Docker network.
	SPOKE_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${container}")
	if [[ -z "${SPOKE_IP}" ]]; then
		echo "ERROR: Could not determine IP for container ${container}"
		exit 1
	fi
	echo "  ${spoke} control-plane IP: ${SPOKE_IP}"

	# Create a selector-less Service in the hub cluster.
	kubectl --context "kind-${HUB_CLUSTER}" apply -f - << EOF
apiVersion: v1
kind: Service
metadata:
  name: ${spoke}
  namespace: default
spec:
  ports:
    - port: ${API_SERVER_PORT}
      targetPort: ${API_SERVER_PORT}
      protocol: TCP
      name: https
  clusterIP: None
EOF

	# Create the matching EndpointSlice pointing at the spoke control plane.
	kubectl --context "kind-${HUB_CLUSTER}" apply -f - << EOF
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: ${spoke}
  namespace: default
  labels:
    kubernetes.io/service-name: ${spoke}
addressType: IPv4
endpoints:
  - addresses:
      - ${SPOKE_IP}
ports:
  - port: ${API_SERVER_PORT}
    protocol: TCP
    name: https
EOF

	# Get the spoke kubeconfig and replace the external server address with the
	# in-cluster service DNS name so the controller can reach it from inside the hub.
	SPOKE_KUBECONFIG=$(kind -- get kubeconfig --name "${spoke}" \
		| yq ".clusters[].cluster.server = \"https://${spoke}.default.svc.cluster.local:${API_SERVER_PORT}\"")

	kubectl --context "kind-${HUB_CLUSTER}" create secret generic "${spoke}" \
		--namespace "${KUBECONFIGS_NAMESPACE}" \
		--from-literal=kubeconfig="${SPOKE_KUBECONFIG}"

	kubectl --context "kind-${HUB_CLUSTER}" label secret "${spoke}" \
		--namespace "${KUBECONFIGS_NAMESPACE}" \
		sigs.k8s.io/multicluster-runtime-kubeconfig=true
done

echo ""
echo "Setup complete."
echo "  Hub cluster : kind-${HUB_CLUSTER}"
for i in $(seq 1 "${NUM_SPOKES}"); do
	echo "  Spoke cluster: kind-${SPOKE_CLUSTERS_PREFIX}${i}"
done
