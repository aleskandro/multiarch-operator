package openshift

import (
	ocpv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"k8s.io/klog/v2"
	"multiarch-operator/pkg/system_config"
)

func ICSPOnAdd(ic system_config.IConfigSyncer) func(obj interface{}) {
	return func(obj interface{}) {
		icsp, ok := obj.(*ocpv1alpha1.ImageContentSourcePolicy)
		if !ok {
			// TODO[informers]: should we panic here?
			klog.Errorf("unexpected type %T, expected ImageContentSourcePolicy", obj)
			return
		}
		for _, source := range icsp.Spec.RepositoryDigestMirrors {
			err := ic.UpdateRegistryMirroringConfig(source.Source, source.Mirrors)
			if err != nil {
				// TODO[icsp]: what to do if we fail to update registry mirroring config?
				klog.Warningf("error updating registry mirroring config %s's source %s : %w",
					icsp.Name, source.Source, err)
				continue
			}
		}
	}
}

func ICSPOnDelete(ic system_config.IConfigSyncer) func(obj interface{}) {
	return func(obj interface{}) {
		icsp, ok := obj.(*ocpv1alpha1.ImageContentSourcePolicy)
		if !ok {
			// TODO[informers]: should we panic here?
			klog.Errorf("unexpected type %T, expected ImageContentSourcePolicy", obj)
			return
		}
		// TODO is this valid
		for _, source := range icsp.Spec.RepositoryDigestMirrors {
			err := ic.DeleteRegistryMirroringConfig(source.Source)
			if err != nil {
				// TODO
				klog.Warningf("error removing registry mirroring config %s's source %s : %w",
					icsp.Name, source.Source, err)
				continue
			}
		}
	}
}

func ICSPOnUpdate(ic system_config.IConfigSyncer) func(oldobj, newobj interface{}) {
	return func(oldobj, newobj interface{}) {
		ICSPOnDelete(ic)(oldobj)
		ICSPOnAdd(ic)(newobj)
	}
}
