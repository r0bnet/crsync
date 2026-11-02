package controller

import (
	"os"
	"strings"
	"testing"
)

// examplesUsingSourceClusterLabel reference LabelSourceCluster as a string literal.
var examplesUsingSourceClusterLabel = []string{
	"../../examples/crossplane/composition.yaml",
	"../../examples/mutating-admission-policy/policy.yaml",
}

// GUARD: fails when LabelSourceCluster changes but the examples still use the old key.
func TestExamplesReferenceSourceClusterLabel(t *testing.T) {
	for _, path := range examplesUsingSourceClusterLabel {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(content), LabelSourceCluster) {
			t.Errorf("%s does not reference %q: the source-cluster label key changed, so update the example "+
				"and tell users their compositions and policies select on the new key", path, LabelSourceCluster)
		}
	}
}
