package main

import (
	"flag"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/access"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	clusterinventoryapi "sigs.k8s.io/multicluster-runtime/providers/cluster-inventory-api"
	"sigs.k8s.io/multicluster-runtime/providers/cluster-inventory-api/kubeconfigstrategy"
	kindprovider "sigs.k8s.io/multicluster-runtime/providers/kind"

	"github.com/r0bnet/crsync/cmd/config"
	crsynccontroller "github.com/r0bnet/crsync/internal/controller"
	"github.com/r0bnet/crsync/internal/resolver"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(eventsv1.AddToScheme(scheme))
	utilruntime.Must(clusterinventoryv1alpha1.AddToScheme(scheme))
}

func main() {
	var configFile string
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var useKindProvider bool
	var kindHubKubeconfig string
	var kindClustersPrefix string
	var hubMaxConcurrentReconciles int
	var spokeMaxConcurrentReconciles int

	flag.StringVar(&configFile, "config", os.Getenv("CONFIG_FILE"), "Configuration file")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metric endpoint binds to. "+
		"Use :8080 to enable. Disabled by default (0).")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	providerFile := access.SetupProviderFileFlag()
	flag.BoolVar(&useKindProvider, "use-kind-provider", false,
		"Use the kind multicluster provider instead of the ClusterProfile provider. Requires --kind-hub-kubeconfig.")
	flag.StringVar(&kindHubKubeconfig, "kind-hub-kubeconfig", "",
		"Path to the kubeconfig file for the hub kind cluster. Required when --use-kind-provider is set.")
	flag.StringVar(&kindClustersPrefix, "kind-clusters-prefix", "",
		"Optional prefix used by the kind provider to filter clusters by name.")
	flag.IntVar(&hubMaxConcurrentReconciles, "hub-max-concurrent-reconciles", 1,
		"Maximum number of concurrent reconciles for hub-side controllers (resource sync and secrets).")
	flag.IntVar(&spokeMaxConcurrentReconciles, "spoke-max-concurrent-reconciles", 1,
		"Maximum number of concurrent reconciles for spoke-side controllers.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if configFile == "" {
		setupLog.Error(fmt.Errorf("config file required"), "expected --config flag or CONFIG_FILE env var")
		os.Exit(1)
	}

	ctrlConfig, err := getConfig(configFile)
	if err != nil {
		setupLog.Error(err, "unable to load config", "configFile", configFile)
		os.Exit(1)
	}

	if len(ctrlConfig.Resources) == 0 {
		setupLog.Error(fmt.Errorf("no resources configured"), "config must contain at least one resource GVK")
		os.Exit(1)
	}

	// Build the REST config and select the multicluster provider.
	var (
		restConfig        *rest.Config
		provider          multicluster.Provider
		inventoryProvider *clusterinventoryapi.Provider
	)

	// the kind provider is only intended for local testing and development
	if useKindProvider {
		if kindHubKubeconfig == "" {
			setupLog.Error(fmt.Errorf("--kind-hub-kubeconfig is required"), "missing flag when --use-kind-provider is set")
			os.Exit(1)
		}

		var err error
		restConfig, err = clientcmd.BuildConfigFromFlags("", kindHubKubeconfig)
		if err != nil {
			setupLog.Error(err, "unable to build rest config from kind hub kubeconfig", "path", kindHubKubeconfig)
			os.Exit(1)
		}

		provider = kindprovider.New(kindprovider.Options{Prefix: kindClustersPrefix})
		setupLog.Info("using kind provider", "hubKubeconfig", kindHubKubeconfig, "prefix", kindClustersPrefix)
	} else {
		if *providerFile == "" {
			setupLog.Error(fmt.Errorf("--clusterprofile-provider-file is required"), "missing access provider config for the ClusterProfile provider")
			os.Exit(1)
		}

		accessCfg, err := access.NewFromFile(*providerFile)
		if err != nil {
			setupLog.Error(err, "unable to load access provider config", "path", *providerFile)
			os.Exit(1)
		}

		restConfig = ctrl.GetConfigOrDie()
		inventoryProvider, err = clusterinventoryapi.New(clusterinventoryapi.Options{
			KubeconfigStrategyOption: kubeconfigstrategy.Option{
				AccessProvider: &kubeconfigstrategy.AccessProviderOption{
					Provider: accessCfg,
				},
			},
		})
		if err != nil {
			setupLog.Error(err, "unable to create ClusterProfile provider")
			os.Exit(1)
		}
		provider = inventoryProvider
		setupLog.Info("using ClusterProfile provider", "providerFile", *providerFile)
	}

	mgr, err := mcmanager.New(restConfig, provider, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "crsync.r0bnet.github.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to create multicluster manager")
		os.Exit(1)
	}

	// The ClusterProfile provider watches ClusterProfiles through the manager's hub client.
	if inventoryProvider != nil {
		if err := inventoryProvider.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to set up ClusterProfile provider")
			os.Exit(1)
		}
	}

	// Resolve configured resources to concrete GVKs. Resources without a
	// version are expanded to all versions found on the API server.
	disc, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "unable to create discovery client")
		os.Exit(1)
	}

	gvkResolver := resolver.NewGVKResolver(disc)
	resolvedGVKs, err := gvkResolver.Resolve(ctrlConfig.Resources)
	if err != nil {
		setupLog.Error(err, "unable to resolve resource GVKs")
		os.Exit(1)
	}

	if len(resolvedGVKs) == 0 {
		setupLog.Error(fmt.Errorf("no GVKs resolved"), "config resources did not resolve to any concrete GVKs")
		os.Exit(1)
	}

	// Register a controller for each resolved GVK.
	for _, gvk := range resolvedGVKs {
		fromHubReconciler := crsynccontroller.NewCRSyncFromHubReconciler(mgr.GetLocalManager().GetClient(), mgr, gvk)
		if err := fromHubReconciler.SetupWithManager(mgr, hubMaxConcurrentReconciles); err != nil {
			setupLog.Error(err, "unable to create controller", "gvk", gvk.String())
			os.Exit(1)
		}
		setupLog.Info("registered controller", "gvk", gvk.String(), "type", "hub")

		fromSpokeReconciler := crsynccontroller.NewCRSyncFromSpokeReconciler(mgr.GetLocalManager().GetClient(), mgr, gvk, mgr.GetLocalManager().GetEventRecorder("crsync"))
		if err := fromSpokeReconciler.SetupWithManager(mgr, spokeMaxConcurrentReconciles); err != nil {
			setupLog.Error(err, "unable to create controller", "gvk", gvk.String())
			os.Exit(1)
		}
		setupLog.Info("registered controller", "gvk", gvk.String(), "type", "spoke")
	}

	// Build a set of watched GVKs for the event filter.
	watchedGVKs := make(map[schema.GroupVersionKind]struct{}, len(resolvedGVKs))
	for _, gvk := range resolvedGVKs {
		watchedGVKs[gvk] = struct{}{}
	}

	// Register a single event controller that watches events.k8s.io/v1 Events
	// and filters to only those whose Regarding object matches a configured GVK.
	eventReconciler := crsynccontroller.NewEventReconciler(watchedGVKs)
	if err := eventReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create event controller")
		os.Exit(1)
	}
	setupLog.Info("registered event controller")

	// Build allowed secret types from config.
	allowedSecretTypes := make([]corev1.SecretType, len(ctrlConfig.SecretTypes))
	for i, t := range ctrlConfig.SecretTypes {
		allowedSecretTypes[i] = corev1.SecretType(t)
	}

	// Register the secret controller (hub-only, namespace-label-filtered).
	secretReconciler := crsynccontroller.NewSecretReconciler(mgr.GetLocalManager().GetClient(), mgr, crsynccontroller.WithNamespaceLabel(crsynccontroller.LabelSourceCluster))
	if err := secretReconciler.SetupWithManager(mgr, allowedSecretTypes, hubMaxConcurrentReconciles); err != nil {
		setupLog.Error(err, "unable to create secret controller")
		os.Exit(1)
	}
	setupLog.Info("registered secret controller", "namespaceLabel", crsynccontroller.LabelSourceCluster)

	// Register a runnable that cleans up hub namespaces belonging to a spoke
	// cluster when it is disengaged.
	nsDeprovisioner := crsynccontroller.NewNamespaceDeprovisioner(
		mgr.GetLocalManager().GetClient(),
	)
	if err := mgr.Add(nsDeprovisioner); err != nil {
		setupLog.Error(err, "unable to add namespace provisioner")
		os.Exit(1)
	}
	setupLog.Info("registered namespace provisioner")

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"resolvedGVKCount", len(resolvedGVKs),
	)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func getConfig(configFile string) (*config.ControllerConfig, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("unable to read config file %s: %w", configFile, err)
	}
	setupLog.Info("config file loaded", "content", string(data))
	return config.ReadConfig(data)
}
