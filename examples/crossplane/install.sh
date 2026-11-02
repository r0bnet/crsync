#!/usr/bin/env bash
# Installs Crossplane and the XDatabase example on the hub of the e2e kind setup.
# Run test/hack/setup-e2e-environment.sh first.

set -euo pipefail

EXAMPLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HUB_CONTEXT="kind-${HUB_CLUSTER:-crsynchub}"
SPOKE_PREFIX="${SPOKE_CLUSTERS_PREFIX:-crsyncspoke}"
PROFILE_NAMESPACE="${PROFILE_NAMESPACE:-fleet}"
RELEASE_NAMESPACE="${RELEASE_NAMESPACE:-crsync-system}"
CROSSPLANE_VERSION="2.4.3"
FUNCTION_PT_VERSION="v0.11.2"
PROVIDER_KUBERNETES_VERSION="v1.4.1"
XR_CRD="xdatabases.demo.crsync.io"

hub() { kubectl --context "${HUB_CONTEXT}" "$@"; }

echo "=== Installing Crossplane ${CROSSPLANE_VERSION} on the hub"
helm repo add crossplane-stable https://charts.crossplane.io/stable --force-update > /dev/null
helm upgrade --install crossplane crossplane-stable/crossplane \
	--kube-context "${HUB_CONTEXT}" \
	--namespace crossplane-system --create-namespace \
	--version "${CROSSPLANE_VERSION}" \
	--wait --timeout 300s

echo "=== Installing function-patch-and-transform and provider-kubernetes"
hub apply -f - << EOF
apiVersion: pkg.crossplane.io/v1beta1
kind: Function
metadata:
  name: function-patch-and-transform
spec:
  package: xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:${FUNCTION_PT_VERSION}
---
apiVersion: pkg.crossplane.io/v1beta1
kind: DeploymentRuntimeConfig
metadata:
  name: provider-kubernetes
spec:
  serviceAccountTemplate:
    metadata:
      name: provider-kubernetes
---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubernetes
spec:
  package: xpkg.crossplane.io/crossplane-contrib/provider-kubernetes:${PROVIDER_KUBERNETES_VERSION}
  runtimeConfigRef:
    apiVersion: pkg.crossplane.io/v1beta1
    kind: DeploymentRuntimeConfig
    name: provider-kubernetes
---
# Demo only: the provider manages objects in the hub itself.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: provider-kubernetes-cluster-admin
subjects:
  - kind: ServiceAccount
    name: provider-kubernetes
    namespace: crossplane-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
EOF
hub wait --for=condition=Healthy function/function-patch-and-transform provider/provider-kubernetes --timeout=300s

echo "=== Applying XRD and Composition"
hub apply -f "${EXAMPLE_DIR}/xrd.yaml" -f "${EXAMPLE_DIR}/composition.yaml"
hub wait --for=condition=Established xrd/xdatabases.demo.crsync.io --timeout=120s
hub wait --for=condition=Established "crd/${XR_CRD}" --timeout=120s

echo "=== Protecting Crossplane's fields on hub copies"
sed "s/system:serviceaccount:crsync-system:/system:serviceaccount:${RELEASE_NAMESPACE}:/" "${EXAMPLE_DIR}/hub-policy.yaml" | hub apply -f -

# Stands in for the central controller: one ClusterProviderConfig per inventory cluster,
# named like crsync's source-cluster label (<profile-namespace>.<profile-name>).
echo "=== Creating one ClusterProviderConfig per ClusterProfile in ${PROFILE_NAMESPACE}"
for profile in $(hub get clusterprofiles -n "${PROFILE_NAMESPACE}" -o jsonpath='{.items[*].metadata.name}'); do
	hub apply -f - << EOF
apiVersion: kubernetes.m.crossplane.io/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: ${PROFILE_NAMESPACE}.${profile}
spec:
  credentials:
    source: InjectedIdentity
EOF
done

# Stands in for the cluster manager (e.g. Sveltos): users get the XR API without Crossplane's
# fields, so the workload cluster's API server prunes spec.crossplane before crsync sees it.
echo "=== Copying the stripped XR CRD to the workload clusters"
XR_CRD_YAML=$(hub get crd "${XR_CRD}" -o yaml | yq 'del(.metadata.ownerReferences, .metadata.resourceVersion, .metadata.uid,
	.metadata.creationTimestamp, .metadata.generation, .metadata.managedFields, .metadata.annotations, .status)
	| del(.spec.versions[].schema.openAPIV3Schema.properties.spec.properties.crossplane)
	| del(.spec.versions[].schema.openAPIV3Schema.properties.status.properties.crossplane)')
for ctx in $(kubectl config get-contexts -o name | grep -E "^kind-${SPOKE_PREFIX}[0-9]+$"); do
	echo "${XR_CRD_YAML}" | kubectl --context "${ctx}" apply -f -
	kubectl --context "${ctx}" wait --for=condition=Established "crd/${XR_CRD}" --timeout=60s
done

echo "=== Adding XDatabase to crsync's synced resources and writing back connection Secrets"
helm upgrade crsync "$(git -C "${EXAMPLE_DIR}" rev-parse --show-toplevel)/charts/crsync" \
	--kube-context "${HUB_CONTEXT}" \
	--namespace "${RELEASE_NAMESPACE}" \
	--reuse-values \
	--set-json 'config.resources=[{"group":"crsync.test.io","kind":"CRSyncTest","resource":"crsynctests"},{"group":"demo.crsync.io","kind":"XDatabase","resource":"xdatabases"}]' \
	--set-json 'config.writeBack=[{"group":"","version":"v1","kind":"Secret","resource":"secrets"}]' \
	--wait --timeout 120s

echo ""
echo "Done. Try: kubectl --context kind-${SPOKE_PREFIX}1 apply -f ${EXAMPLE_DIR}/xdatabase.yaml"
