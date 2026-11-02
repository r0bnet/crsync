package controller

import (
	"os"
	"strings"
	"testing"
)

// examplesUsingSourceClusterLabel reference LabelSourceCluster as a string literal.
var examplesUsingSourceClusterLabel = []string{
	"../../examples/crossplane/composition.yaml",
	"../../examples/crossplane/hub-policy.yaml",
	"../../examples/mutating-admission-policy/policy.yaml",
	"../../examples/mutating-admission-policy-prefix/policy.yaml",
	"../../examples/mutating-webhook/deploy.yaml",
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

// GUARD: fails when AnnotationWriteBack changes but the docs still tell users the old key.
func TestDocsReferenceWriteBackAnnotation(t *testing.T) {
	for _, path := range []string{"../../README.md", "../../examples/crossplane/README.md"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(content), AnnotationWriteBack) {
			t.Errorf("%s does not reference %q: the write-back annotation key changed, so update the docs "+
				"and tell users their compositions and policies must set the new key", path, AnnotationWriteBack)
		}
	}
}
