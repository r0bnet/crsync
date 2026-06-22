package resolver

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/r0bnet/crsync/cmd/config"
)

// GVKResolver resolves configured resource GVKs against the API server.
// Resources that already specify a version are kept as-is. Resources with an
// empty version are expanded to every version the API server reports for that
// Group and Kind.
type GVKResolver struct {
	disc discovery.DiscoveryInterface
}

// NewGVKResolver returns a GVKResolver backed by the given discovery client.
func NewGVKResolver(disc discovery.DiscoveryInterface) *GVKResolver {
	return &GVKResolver{disc: disc}
}

// Resolve expands the given resources into concrete GVKs. Resources with a
// non-empty version are returned unchanged. Resources with an empty version are
// looked up via the discovery API and expanded to all matching versions.
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

	_, apiResourceLists, err := r.disc.ServerGroupsAndResources()
	if err != nil {
		return nil, fmt.Errorf("discovering server resources: %w", err)
	}

	for _, res := range versionsToBeResolved {
		var found bool
		for _, rl := range apiResourceLists {
			gv, parseErr := schema.ParseGroupVersion(rl.GroupVersion)
			if parseErr != nil || gv.Group != res.Group {
				continue
			}
			for _, apiRes := range rl.APIResources {
				if apiRes.Kind == res.Kind {
					resolved = append(resolved, schema.GroupVersionKind{
						Group:   res.Group,
						Version: gv.Version,
						Kind:    res.Kind,
					})
					found = true
					break
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("no versions found for group=%q kind=%q", res.Group, res.Kind)
		}
	}

	return resolved, nil
}
