package resolver

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/r0bnet/crsync/cmd/config"
)

// GVKResolver resolves configured resource GVKs against the API server.
// Resources that already specify a version are kept as-is. Resources with an
// empty version get the group's preferred version that serves the Kind.
type GVKResolver struct {
	disc discovery.DiscoveryInterface
}

// NewGVKResolver returns a GVKResolver backed by the given discovery client.
func NewGVKResolver(disc discovery.DiscoveryInterface) *GVKResolver {
	return &GVKResolver{disc: disc}
}

// Resolve returns one concrete GVK per resource. One version per Kind keeps two
// controllers from writing the same objects through different versions.
func (r *GVKResolver) Resolve(resources []config.ResourceGVK) ([]schema.GroupVersionKind, error) {
	var (
		resolved             []schema.GroupVersionKind
		versionsToBeResolved []config.ResourceGVK
	)
	for _, res := range resources {
		if res.Version != "" {
			resolved = append(resolved, res.ToGVK())
		} else {
			versionsToBeResolved = append(versionsToBeResolved, res)
		}
	}

	if len(versionsToBeResolved) == 0 {
		return resolved, nil
	}

	groups, apiResourceLists, err := r.disc.ServerGroupsAndResources()
	if err != nil {
		return nil, fmt.Errorf("discovering server resources: %w", err)
	}

	served := map[schema.GroupVersionKind]bool{}
	for _, rl := range apiResourceLists {
		gv, parseErr := schema.ParseGroupVersion(rl.GroupVersion)
		if parseErr != nil {
			continue
		}
		for _, apiRes := range rl.APIResources {
			served[gv.WithKind(apiRes.Kind)] = true
		}
	}

	for _, res := range versionsToBeResolved {
		gvk, ok := preferredGVK(groups, served, res)
		if !ok {
			return nil, fmt.Errorf("no versions found for group=%q kind=%q", res.Group, res.Kind)
		}
		resolved = append(resolved, gvk)
	}

	return resolved, nil
}

// preferredGVK returns the group's preferred version if it serves the Kind,
// otherwise the first served version in the server's priority order.
func preferredGVK(groups []*metav1.APIGroup, served map[schema.GroupVersionKind]bool, res config.ResourceGVK) (schema.GroupVersionKind, bool) {
	for _, group := range groups {
		if group.Name != res.Group {
			continue
		}
		versions := append([]metav1.GroupVersionForDiscovery{group.PreferredVersion}, group.Versions...)
		for _, v := range versions {
			gvk := schema.GroupVersionKind{Group: res.Group, Version: v.Version, Kind: res.Kind}
			if served[gvk] {
				return gvk, true
			}
		}
	}
	return schema.GroupVersionKind{}, false
}
