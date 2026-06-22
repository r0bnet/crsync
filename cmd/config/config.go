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
	// SecretTypes is an optional list of Secret types the secret controller
	// should reconcile (e.g. "Opaque", "kubernetes.io/tls"). When empty, all
	// Secret types are reconciled.
	SecretTypes []string `json:"secretTypes,omitempty"`
}

// ReadConfig parses YAML data into a ControllerConfig.
func ReadConfig(data []byte) (*ControllerConfig, error) {
	cfg := ControllerConfig{}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("unable to unmarshal config: %w", err)
	}
	return &cfg, nil
}
