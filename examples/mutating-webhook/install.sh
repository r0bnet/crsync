#!/usr/bin/env bash
# Deploys the mutating webhook example to the hub of the e2e kind setup.
# Run test/hack/setup-e2e-environment.sh first.

set -euo pipefail

EXAMPLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(git -C "${EXAMPLE_DIR}" rev-parse --show-toplevel)"
HUB_CLUSTER="${HUB_CLUSTER:-crsynchub}"
HUB_CONTEXT="kind-${HUB_CLUSTER}"
SPOKE_PREFIX="${SPOKE_CLUSTERS_PREFIX:-crsyncspoke}"
RELEASE_NAMESPACE="${RELEASE_NAMESPACE:-crsync-system}"
IMAGE="r0bnet/crsync-webhook:e2e"
SERVICE="crsync-webhook.crsync-webhook.svc"

hub() { kubectl --context "${HUB_CONTEXT}" "$@"; }

echo "=== Building and loading the webhook image"
docker build -t "${IMAGE}" -f "${EXAMPLE_DIR}/Dockerfile" "${WORKSPACE}"
kind load docker-image "${IMAGE}" --name "${HUB_CLUSTER}"

echo "=== Creating a self-signed serving certificate"
CERT_DIR="$(mktemp -d)"
trap 'rm -rf "${CERT_DIR}"' EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 365 -subj "/CN=${SERVICE}" \
	-addext "subjectAltName=DNS:${SERVICE}" \
	-keyout "${CERT_DIR}/tls.key" -out "${CERT_DIR}/tls.crt" 2> /dev/null
hub create namespace crsync-webhook --dry-run=client -o yaml | hub apply -f -
hub -n crsync-webhook create secret tls crsync-webhook-tls \
	--cert "${CERT_DIR}/tls.crt" --key "${CERT_DIR}/tls.key" --dry-run=client -o yaml | hub apply -f -

echo "=== Applying the Queue CRD to all clusters"
for ctx in "${HUB_CONTEXT}" $(kubectl config get-contexts -o name | grep -E "^kind-${SPOKE_PREFIX}[0-9]+$"); do
	kubectl --context "${ctx}" apply -f "${EXAMPLE_DIR}/crd.yaml"
done

echo "=== Deploying the webhook"
sed "s/CA_BUNDLE/$(base64 < "${CERT_DIR}/tls.crt" | tr -d '\n')/" "${EXAMPLE_DIR}/deploy.yaml" | hub apply -f -
hub -n crsync-webhook rollout restart deployment/crsync-webhook
hub -n crsync-webhook rollout status deployment/crsync-webhook --timeout=120s

echo "=== Adding Queue to crsync's synced resources"
RESOURCES=$(helm get values crsync --kube-context "${HUB_CONTEXT}" -n "${RELEASE_NAMESPACE}" -o json \
	| jq -c '.config.resources + [{"group":"messaging.example.io","kind":"Queue","resource":"queues"}] | unique')
helm upgrade crsync "${WORKSPACE}/charts/crsync" \
	--kube-context "${HUB_CONTEXT}" \
	--namespace "${RELEASE_NAMESPACE}" \
	--reuse-values \
	--set-json "config.resources=${RESOURCES}" \
	--wait --timeout 120s

echo ""
echo "Done. Try:"
echo "  kubectl --context kind-${SPOKE_PREFIX}1 label namespace default example.io/cost-center=cc-42"
echo "  kubectl --context kind-${SPOKE_PREFIX}1 apply -f ${EXAMPLE_DIR}/queue.yaml"
echo "  kubectl --context ${HUB_CONTEXT} -n fleet-${SPOKE_PREFIX}1--default get queue orders -o jsonpath='{.spec.costCenter}'"
