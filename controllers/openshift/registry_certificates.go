package openshift

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"multiarch-operator/pkg/system_config"
)

func registryCertificatesConfigMapOnAddOrUpdate(ic system_config.IConfigSyncer, obj interface{}) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		// TODO[informers]: should we panic here?
		klog.Errorf("unexpected type %T, expected ConfigMap", obj)
		return
	}
	if cm.Name != "image-registry-certificates" {
		// Ignore other configmaps
		return
	}
	klog.Warningln("the image-registry-certificates configmap has been updated.")
	err := ic.StoreRegistryCerts(system_config.ParseRegistryCerts(cm.Data))
	if err != nil {
		klog.Warningf("error updating registry certs: %w", err)
		return
	}
}

func RegistryCertificatesConfigMapOnUpdate(ic system_config.IConfigSyncer) func(oldobj, newobj interface{}) {
	return func(oldobj, newobj interface{}) {
		registryCertificatesConfigMapOnAddOrUpdate(ic, newobj)
	}
}

func RegistryCertificatesConfigMapOnAdd(ic system_config.IConfigSyncer) func(obj interface{}) {
	return func(obj interface{}) {
		registryCertificatesConfigMapOnAddOrUpdate(ic, obj)
	}
}
