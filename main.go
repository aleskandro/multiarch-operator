/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	ocpv1 "github.com/openshift/api/config/v1"
	ocpv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	v12 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"multiarch-operator/controllers/core"
	"multiarch-operator/controllers/openshift"
	"multiarch-operator/pkg/system_config"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/webhook"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	multiarchv1alpha1 "multiarch-operator/apis/multiarch/v1alpha1"
	"multiarch-operator/controllers"
	multiarchcontrollers "multiarch-operator/controllers/multiarch"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

const readonlySystemConfigResyncPeriod = 30 * time.Minute

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(multiarchv1alpha1.AddToScheme(scheme))

	// TODO[OCP specific]
	utilruntime.Must(ocpv1.Install(scheme))
	utilruntime.Must(ocpv1alpha1.Install(clientgoscheme.Scheme))

	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	klog.InitFlags(nil)
	flag.Set("alsologtostderr", "true")

	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "208d7abd.multiarch.openshift.io",
		CertDir:                "/var/run/manager/tls",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	config := ctrl.GetConfigOrDie()
	clientset := kubernetes.NewForConfigOrDie(config)

	if err = (&controllers.PodReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Clientset: clientset,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Pod")
		os.Exit(1)
	}
	if err = (&multiarchcontrollers.PodPlacementConfigReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Clientset: clientset,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PodPlacementConfig")
		os.Exit(1)
	}

	// TODO[OCP specific]
	initializeOCPSystemConfigSyncerInformersWatchers(mgr, clientset)

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	mgr.GetWebhookServer().Register("/add-pod-scheduling-gate", &webhook.Admission{Handler: &controllers.PodSchedulingGateMutatingWebHook{
		Client: mgr.GetClient(),
	}})

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initializeOCPSystemConfigSyncerInformersWatchers(mgr manager.Manager, clientset *kubernetes.Clientset) {
	ctx := context.Background()
	ic := system_config.SystemConfigSyncerSingleton()
	// Watch ICSPs and Sync SystemConfig
	icspInformer, err := mgr.GetCache().GetInformerForKind(ctx, ocpv1alpha1.GroupVersion.WithKind("ImageContentSourcePolicy"))
	_, err = icspInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    openshift.ICSPOnAdd(ic),
		UpdateFunc: openshift.ICSPOnUpdate(ic),
		DeleteFunc: openshift.ICSPOnDelete(ic),
	})
	if err != nil {
		// TODO[informers] handle the error
		return
	}
	// Trigger an initial add for each existing ICSP
	icspList := ocpv1alpha1.ImageContentSourcePolicyList{}
	err = mgr.GetClient().List(ctx, &icspList)
	if err != nil {
		// TODO[informers] handle the error
		return
	}
	for _, obj := range icspList.Items {
		openshift.ICSPOnAdd(ic)(&obj)
	}

	registryCertificatesInformer := v12.NewConfigMapInformer(clientset, "openshift-image-registry", 0, cache.Indexers{})
	handler, err := registryCertificatesInformer.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    openshift.RegistryCertificatesConfigMapOnAdd(ic),
			UpdateFunc: openshift.RegistryCertificatesConfigMapOnUpdate(ic),
		},
	)
	if err != nil {
		return
	}
	// Trigger an initial add for the existing registry certificates configmap
	registryCertsConfigMap, err := clientset.CoreV1().ConfigMaps("openshift-image-registry").Get(ctx, "image-registry-certificates", metav1.GetOptions{})
	if err != nil {
		// TODO[informers] handle the error
		return
	}
	openshift.RegistryCertificatesConfigMapOnAdd(ic)(registryCertsConfigMap)

	err = core.NewSingleObjectEventHandler[*ocpv1.Image, *ocpv1.ImageList](ctx,
		"cluster", "", time.Hour,
		func(et watch.EventType, image *ocpv1.Image) {
			if et == watch.Deleted || et == watch.Bookmark {
				klog.Warningf("Ignoring event type: %+v", et)
				return
			}
			klog.Warningln("the image.config.openshift.io/cluster object has been updated.")
			err := ic.StoreImageRegistryConf(image.Spec.RegistrySources.AllowedRegistries,
				image.Spec.RegistrySources.BlockedRegistries, image.Spec.RegistrySources.InsecureRegistries)
			if err != nil {
				klog.Warningf("error updating registry conf: %w", err)
				return
			}
		}, nil)
	if err != nil {
		klog.Fatalf("error registering handler for the image.config.openshift.io/cluster object: %w", err)
	}
}
