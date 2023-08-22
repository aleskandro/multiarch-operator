package system_config

import (
	"fmt"
	"k8s.io/klog/v2"
	"os"
	"sync"
)

var (
	singletonSystemConfigInstance IConfigSyncer
	once                          sync.Once
)

type SystemConfigSyncer struct {
	registriesConfContent registriesConf
	policyConfContent     policyConf
	registryCertTuples    []registryCertTuple

	ch chan bool
	mu sync.Mutex
}

// SystemConfigSyncerSingleton returns the singleton instance of the SystemConfigSyncer
func SystemConfigSyncerSingleton() IConfigSyncer {
	once.Do(func() {
		singletonSystemConfigInstance = newSystemConfigSyncer()
	})
	return singletonSystemConfigInstance
}

func (s *SystemConfigSyncer) StoreImageRegistryConf(allowedRegistries []string, blockedRegistries []string, insecureRegistries []string) error {
	if len(allowedRegistries) > 0 && len(blockedRegistries) > 0 {
		return fmt.Errorf("only one of allowedRegistries and blockedRegistries can be set. Ignoring this event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Ensure the previous state is reset
	for _, rc := range s.registriesConfContent.Registries {
		rc.Allowed = nil
		rc.Blocked = nil
		rc.Insecure = nil
	}
	s.policyConfContent.resetTransports()
	// At the time of writing, we don't see the need to generate multiple bool pointers. Keeping it the same, but at
	// the registryConf level.
	trueValue := true
	for _, registry := range allowedRegistries {
		rc := s.registriesConfContent.getRegistryConfOrCreate(registry)
		rc.Allowed = &trueValue
		rc.Blocked = nil
	}
	for _, registry := range blockedRegistries {
		rc := s.registriesConfContent.getRegistryConfOrCreate(registry)
		rc.Allowed = nil
		rc.Blocked = &trueValue
		s.policyConfContent.setRejectForRegistry(registry)
	}
	for _, registry := range insecureRegistries {
		rc := s.registriesConfContent.getRegistryConfOrCreate(registry)
		rc.Insecure = &trueValue
	}
	s.ch <- true
	return nil
}

func (s *SystemConfigSyncer) StoreRegistryCerts(registryCertTuples []registryCertTuple) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registryCertTuples = registryCertTuples
	s.ch <- true
	return nil
}

func (s *SystemConfigSyncer) UpdateRegistryMirroringConfig(registry string, mirrors []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rc := s.registriesConfContent.getRegistryConfOrCreate(registry)
	rc.Mirrors = mirrors
	s.ch <- true
	return nil
}

func (s *SystemConfigSyncer) DeleteRegistryMirroringConfig(registry string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rc, ok := s.registriesConfContent.getRegistryConf(registry); ok {
		rc.Mirrors = []string{}
		s.ch <- true
		return nil
	}
	return fmt.Errorf("registry %s not found", registry)
}

func (s *SystemConfigSyncer) CleanupRegistryMirroringConfig() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, registry := range s.registriesConfContent.Registries {
		registry.Mirrors = []string{}
	}
	s.ch <- true
	return nil
}

func (s *SystemConfigSyncer) sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// marshall registries.conf and write to file
	if err := s.registriesConfContent.writeToFile(); err != nil {
		klog.Errorf("error writing registries.conf: %v", err)
		return err
	}
	// marshall policy.json and write to file
	if err := s.policyConfContent.writeToFile(); err != nil {
		klog.Errorf("error writing policy.json: %v", err)
		return err
	}
	// delete the certs.d content
	if err := os.RemoveAll(DockerCertsDir); err != nil {
		klog.Errorf("error deleting certs.d directory: %v", err)
		return err
	}
	// write registry certs to file
	for _, tuple := range s.registryCertTuples {
		if err := tuple.writeToFile(); err != nil {
			klog.Errorf("error writing registry cert: %v", err)
			return err
		}
	}
	return nil
}

// this should launch as a goroutine to consume events from the channel and write to disk
func (s *SystemConfigSyncer) syncer() {
	for {
		select {
		case <-s.ch:
			if err := s.sync(); err != nil {
				klog.Errorf("error syncing system config: %v", err)
			}
		}
	}
}

//+kubebuilder:rbac:groups=core,resources=configmap,verbs=get;list;watch,namespace="openshift-config"
//+kubebuilder:rbac:groups=core,resources=configmap,verbs=get;list;watch,namespace="openshift-image-registry"
//+kubebuilder:rbac:groups=config.openshift.io,resources=images,verbs=get;list;watch
//+kubebuilder:rbac:groups=operator.openshift.io,resources=imagecontentsourcepolicies,verbs=get;list;watch

// newSystemConfigSyncer creates a new SystemConfigSyncer object
func newSystemConfigSyncer() IConfigSyncer {
	ic := &SystemConfigSyncer{
		registriesConfContent: defaultRegistriesConf(),
		policyConfContent:     defaultPolicyConf(),
		registryCertTuples:    []registryCertTuple{},
		ch:                    make(chan bool),
	}
	go ic.syncer()
	return ic
}

// ParseRegistryCerts parses the registry certs from a map of registry url to cert
// This map, in ocp, is stored in the data field of the configmap "image-registry-certifiates" in the
// openshift-image-registry namespace.
func ParseRegistryCerts(dataMap map[string]string) []registryCertTuple {
	var registryCertTuples []registryCertTuple
	for k, v := range dataMap {
		registryCertTuples = append(registryCertTuples, registryCertTuple{
			registry: k,
			cert:     v,
		})
	}
	return registryCertTuples
}
