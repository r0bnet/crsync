package config

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// ResourceGVK defines a Group/Version/Kind tuple to watch across clusters.
// Version is optional; when empty, all available versions for the given
// Group and Kind are discovered from the API server.
type ResourceGVK struct {
	Group   string `json:"group"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind"`
}

// ToGVK converts the config type to a schema.GroupVersionKind.
func (r ResourceGVK) ToGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: r.Group, Version: r.Version, Kind: r.Kind}
}

// ControllerConfig holds the crsync controller configuration.
type ControllerConfig struct {
	// Resources is the list of GVKs the controller will watch across all clusters.
	Resources []ResourceGVK `json:"resources"`
	// WriteBack lists hub GVKs copied back to the workload cluster when they belong to a
	// hub copy (owner chain or write-back annotation). Empty disables write-back.
	WriteBack []ResourceGVK `json:"writeBack,omitempty"`
	// WriteBackCopyLabel is the label key marking crsync's copies on workload clusters.
	// Empty means the default; changing it orphans existing copies.
	WriteBackCopyLabel string `json:"writeBackCopyLabel,omitempty"`
}

// ReadConfig parses YAML data into a ControllerConfig.
func ReadConfig(data []byte) (*ControllerConfig, error) {
	cfg := ControllerConfig{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unable to unmarshal config: %w", err)
	}
	return &cfg, nil
}
