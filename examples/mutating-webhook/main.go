// Command mutating-webhook sets spec.costCenter on hub copies of Queues from a label on the source
// namespace in the workload cluster, which it reads through the ClusterProfile's access provider.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/access"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/r0bnet/crsync/internal/controller"
)

// costCenterLabel is read from the source namespace on the workload cluster.
const costCenterLabel = "example.io/cost-center"

// handler mutates hub copies. spokeReader is a field so tests can replace the access provider.
type handler struct {
	hub         client.Reader
	spokeReader func(profile *clusterinventoryv1alpha1.ClusterProfile) (client.Reader, error)
}

func (h *handler) Handle(ctx context.Context, req admission.Request) admission.Response {
	costCenter, err := h.costCenter(ctx, req.Namespace)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if costCenter == "" {
		return admission.Allowed("source namespace has no " + costCenterLabel + " label")
	}

	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(req.Object.Raw); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := unstructured.SetNestedField(obj.Object, costCenter, "spec", "costCenter"); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	mutated, err := json.Marshal(obj.Object)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, mutated)
}

// costCenter follows the hub namespace to its ClusterProfile and reads the source namespace there.
func (h *handler) costCenter(ctx context.Context, hubNamespace string) (string, error) {
	ns := &corev1.Namespace{}
	if err := h.hub.Get(ctx, client.ObjectKey{Name: hubNamespace}, ns); err != nil {
		return "", fmt.Errorf("get hub namespace %s: %w", hubNamespace, err)
	}
	sourceNamespace := ns.Labels[controller.LabelSourceNamespace]
	profileNamespace, profileName, ok := strings.Cut(string(controller.ClusterNameFromLabel(ns.Labels[controller.LabelSourceCluster])), "/")
	if !ok || sourceNamespace == "" {
		return "", fmt.Errorf("hub namespace %s has no crsync source labels", hubNamespace)
	}

	profile := &clusterinventoryv1alpha1.ClusterProfile{}
	if err := h.hub.Get(ctx, client.ObjectKey{Namespace: profileNamespace, Name: profileName}, profile); err != nil {
		return "", fmt.Errorf("get ClusterProfile %s/%s: %w", profileNamespace, profileName, err)
	}
	spoke, err := h.spokeReader(profile)
	if err != nil {
		return "", fmt.Errorf("access cluster %s/%s: %w", profileNamespace, profileName, err)
	}

	source := &corev1.Namespace{}
	if err := spoke.Get(ctx, client.ObjectKey{Name: sourceNamespace}, source); err != nil {
		return "", fmt.Errorf("get source namespace %s in %s/%s: %w", sourceNamespace, profileNamespace, profileName, err)
	}
	return source.Labels[costCenterLabel], nil
}

func main() {
	certDir := flag.String("cert-dir", "/etc/webhook/certs", "Directory with tls.crt and tls.key.")
	providerFile := access.SetupProviderFileFlag()
	flag.Parse()
	ctrl.SetLogger(zap.New())
	log := ctrl.Log.WithName("mutating-webhook")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "unable to build scheme")
		os.Exit(1)
	}
	if err := clusterinventoryv1alpha1.AddToScheme(scheme); err != nil {
		log.Error(err, "unable to build scheme")
		os.Exit(1)
	}

	accessCfg, err := access.NewFromFile(*providerFile)
	if err != nil {
		log.Error(err, "unable to load access provider config", "path", *providerFile)
		os.Exit(1)
	}
	hub, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "unable to create hub client")
		os.Exit(1)
	}

	h := &handler{
		hub: hub,
		// shortcut: a new client (and exec plugin call) per request; cache per ClusterProfile under real load.
		spokeReader: func(profile *clusterinventoryv1alpha1.ClusterProfile) (client.Reader, error) {
			cfg, err := accessCfg.BuildConfigFromCP(profile)
			if err != nil {
				return nil, err
			}
			spoke, err := client.New(cfg, client.Options{Scheme: scheme})
			if err != nil {
				return nil, err
			}
			return spoke, nil
		},
	}

	server := webhook.NewServer(webhook.Options{CertDir: *certDir})
	server.Register("/mutate", &webhook.Admission{Handler: h})
	if err := server.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "webhook server stopped")
		os.Exit(1)
	}
}
