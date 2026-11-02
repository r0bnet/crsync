#!/usr/bin/env bash

set -euo pipefail

HUB_CLUSTER="crsynchub"
SPOKE_CLUSTERS_PREFIX="crsyncspoke"
NUM_SPOKES=1

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

EXISTING_CLUSTERS=$(kind get clusters 2> /dev/null || true)

# ---------------------------------------------------------------------------
# Build the list of spokes to delete.
# ---------------------------------------------------------------------------
SPOKES_TO_DELETE=()

if [[ "${CRSYNC_DELETE_ALL_SPOKES:-}" == "true" ]]; then
	# Match every existing cluster whose name starts with the spoke prefix.
	while IFS= read -r cluster; do
		[[ -z "${cluster}" ]] && continue
		if [[ "${cluster}" == "${SPOKE_CLUSTERS_PREFIX}"* ]]; then
			SPOKES_TO_DELETE+=("${cluster}")
		fi
	done <<< "${EXISTING_CLUSTERS}"
else
	for i in $(seq 1 "${NUM_SPOKES}"); do
		SPOKES_TO_DELETE+=("${SPOKE_CLUSTERS_PREFIX}${i}")
	done
fi

# ---------------------------------------------------------------------------
# Delete spokes in reverse order, then the hub.
# ---------------------------------------------------------------------------
for ((idx = ${#SPOKES_TO_DELETE[@]} - 1; idx >= 0; idx--)); do
	spoke="${SPOKES_TO_DELETE[${idx}]}"
	if echo "${EXISTING_CLUSTERS}" | grep -qx "${spoke}"; then
		echo "Deleting spoke cluster: ${spoke}"
		kind delete cluster --name "${spoke}"
	else
		echo "Spoke cluster ${spoke} does not exist, skipping."
	fi
done

if echo "${EXISTING_CLUSTERS}" | grep -qx "${HUB_CLUSTER}"; then
	echo "Deleting hub cluster: ${HUB_CLUSTER}"
	kind delete cluster --name "${HUB_CLUSTER}"
else
	echo "Hub cluster ${HUB_CLUSTER} does not exist, skipping."
fi

echo "Cleanup complete."
