package util

import (
	"net"
	"strings"
	"reflect"
	"kube-enn-proxy/pkg/proxy"
	"k8s.io/apimachinery/pkg/types"
	"kube-enn-proxy/pkg/watchers"
	api "k8s.io/api/core/v1"

	"github.com/golang/glog"

	"k8s.io/apimachinery/pkg/util/sets"

	"sync"
)

type ServiceInfo struct {
	ClusterIP                net.IP
	Port                     int
	Protocol                 string
	NodePort                 int
	SessionAffinity          bool
	ExternalIPs              []string

	OnlyNodeLocalEndpoints   bool
	healthCheckNodePort      int

}

type ProxyServiceMap map[proxy.ServicePortName]*ServiceInfo

type ServiceChangeMap struct {
	Lock  sync.Mutex
	Items map[types.NamespacedName]*ServiceChange
}

type ServiceChange struct {
	Previous ProxyServiceMap
	Current  ProxyServiceMap
}

type UpdateServiceMapResult struct {
	HcServices    map[types.NamespacedName]uint16
	StaleServices sets.String
}

func BuildServiceMap(oldServiceMap ProxyServiceMap) (ProxyServiceMap, sets.String){

	glog.V(3).Infof("BuildServiceMap")
	newServiceMap := make(ProxyServiceMap)
	//hcPorts := make(map[types.NamespacedName]uint16)

	for _, service := range watchers.ServiceWatchConfig.List(){

		svcName := types.NamespacedName{
			Namespace: service.Namespace,
			Name:      service.Name,
		}

		// if ClusterIP is "None" or empty, skip proxying
		if service.Spec.ClusterIP == "None" || service.Spec.ClusterIP == "" {
			glog.V(3).Infof("Skipping service %s due to clusterIP is null", svcName)
			continue
		}
		// Even if ClusterIP is set, ServiceTypeExternalName services don't get proxied
		if service.Spec.Type == "ExternalName" {
			glog.V(3).Infof("Skipping service %s due to Type=ExternalName", svcName)
			continue
		}

		for i := range service.Spec.Ports {
			servicePort := &service.Spec.Ports[i]

			serviceName := proxy.ServicePortName{
				NamespacedName: svcName,
				Port:           servicePort.Name,
			}

			info := NewServiceInfo(serviceName, servicePort, service)

			oldInfo, exists := oldServiceMap[serviceName]
			equal := reflect.DeepEqual(info, oldInfo)
			if !exists {
				glog.V(1).Infof("Adding new service %q at %s:%d/%s", serviceName, info.ClusterIP, servicePort.Port, servicePort.Protocol)
			} else if !equal {
				glog.V(1).Infof("Updating existing service %q at %s:%d/%s", serviceName, info.ClusterIP, servicePort.Port, servicePort.Protocol)
			}

			//if info.onlyNodeLocalEndpoints {
			//	hcPorts[svcName] = uint16(info.healthCheckNodePort)
			//}

			newServiceMap[serviceName] = info
		}

	}

	//for nsn, port := range hcPorts {
	//	if port == 0 {
	//		glog.Errorf("Service %q has no healthcheck nodeport", nsn)
	//		delete(hcPorts, nsn)
	//	}
	//}

	staleUDPServices := sets.NewString()
	// Remove serviceports missing from the update.
	for name, info := range oldServiceMap {
		if _, exists := newServiceMap[name]; !exists {
			glog.V(3).Infof("Removing service %q", name)
			if info.Protocol == strings.ToLower(string(api.ProtocolUDP)){
				staleUDPServices.Insert(info.ClusterIP.String())
			}
		}
	}

	return newServiceMap, staleUDPServices

}

func NewServiceInfo(serviceName proxy.ServicePortName, port *api.ServicePort, service *api.Service) *ServiceInfo{

	onlyNodeLocalEndpoints := false //currently we do not need this feature
	info := &ServiceInfo{
		ClusterIP:                net.ParseIP(service.Spec.ClusterIP),
		Port:                     int(port.Port),
		Protocol:                 strings.ToLower(string(port.Protocol)),
		NodePort:                 int(port.NodePort),
		ExternalIPs:              make([]string, len(service.Spec.ExternalIPs)),
		OnlyNodeLocalEndpoints:   onlyNodeLocalEndpoints,
	}
	copy(info.ExternalIPs, service.Spec.ExternalIPs)

	if service.Spec.SessionAffinity == "ClientIP"{
		info.SessionAffinity = true
	} else {
		info.SessionAffinity = false
	}

	return info

}

func NewServiceChangeMap() ServiceChangeMap {
	return ServiceChangeMap{
		Items: make(map[types.NamespacedName]*ServiceChange),
	}
}

func (scm *ServiceChangeMap) Update(namespacedName *types.NamespacedName, previous, current *api.Service) bool {
	scm.Lock.Lock()
	defer scm.Lock.Unlock()

	change, exists := scm.Items[*namespacedName]
	if !exists {
		change = &ServiceChange{}
		change.Previous = serviceToServiceMap(previous)
		scm.Items[*namespacedName] = change
	}
	change.Current = serviceToServiceMap(current)
	if reflect.DeepEqual(change.Previous, change.Current) {
		delete(scm.Items, *namespacedName)
	}
	return len(scm.Items) > 0
}

// Translates single Service object to proxyServiceMap.
//
// NOTE: service object should NOT be modified.
func serviceToServiceMap(service *api.Service) ProxyServiceMap {
	if service == nil {
		return nil
	}
	svcName := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	if ShouldSkipService(svcName, service) {
		return nil
	}

	serviceMap := make(ProxyServiceMap)
	for i := range service.Spec.Ports {
		servicePort := &service.Spec.Ports[i]
		svcPortName := proxy.ServicePortName{NamespacedName: svcName, Port: servicePort.Name}
		serviceMap[svcPortName] = NewServiceInfo(svcPortName, servicePort, service)
	}
	return serviceMap
}

func ShouldSkipService(svcName types.NamespacedName, service *api.Service) bool {
	// if ClusterIP is "None" or empty, skip proxying
	if !IsServiceIPSet(service) {
		glog.V(3).Infof("Skipping service %s due to clusterIP = %q", svcName, service.Spec.ClusterIP)
		return true
	}
	// Even if ClusterIP is set, ServiceTypeExternalName services don't get proxied
	if service.Spec.Type == api.ServiceTypeExternalName {
		glog.V(3).Infof("Skipping service %s due to Type=ExternalName", svcName)
		return true
	}
	return false
}

func IsServiceIPSet(service *api.Service) bool {
	return service.Spec.ClusterIP != "None" && service.Spec.ClusterIP != ""
}

// <serviceMap> is updated by this function (based on the given changes).
// <changes> map is cleared after applying them.
func UpdateServiceMap(serviceMap ProxyServiceMap, changes *ServiceChangeMap) (result UpdateServiceMapResult) {
	result.StaleServices = sets.NewString()

	func() {
		changes.Lock.Lock()
		defer changes.Lock.Unlock()
		for _, change := range changes.Items {
			existingPorts := serviceMap.merge(change.Current)
			serviceMap.unmerge(change.Previous, existingPorts, result.StaleServices)
		}
		//changes.Items = make(map[types.NamespacedName]*ServiceChange)
	}()

	// TODO: If this will appear to be computationally expensive, consider
	// computing this incrementally similarly to serviceMap.
	result.HcServices = make(map[types.NamespacedName]uint16)
	for svcPortName, info := range serviceMap {
		if info.healthCheckNodePort != 0 {
			result.HcServices[svcPortName.NamespacedName] = uint16(info.healthCheckNodePort)
		}
	}

	return result
}

func (sm *ProxyServiceMap) merge(other ProxyServiceMap) sets.String {
	existingPorts := sets.NewString()
	for svcPortName, info := range other {
		existingPorts.Insert(svcPortName.Port)
		_, exists := (*sm)[svcPortName]
		if !exists {
			glog.V(1).Infof("Adding new service port %q at %s:%d/%s", svcPortName, info.ClusterIP, info.Port, info.Protocol)
		} else {
			glog.V(1).Infof("Updating existing service port %q at %s:%d/%s", svcPortName, info.ClusterIP, info.Port, info.Protocol)
		}
		(*sm)[svcPortName] = info
	}
	return existingPorts
}

func (sm *ProxyServiceMap) unmerge(other ProxyServiceMap, existingPorts, staleServices sets.String) {
	for svcPortName := range other {
		if existingPorts.Has(svcPortName.Port) {
			continue
		}
		info, exists := (*sm)[svcPortName]
		if exists {
			glog.V(1).Infof("Removing service port %q", svcPortName)
			if info.Protocol == strings.ToLower(string(api.ProtocolUDP)){
				staleServices.Insert(info.ClusterIP.String())
			}
			delete(*sm, svcPortName)
		} else {
			glog.Errorf("Service port %q removed, but doesn't exists", svcPortName)
		}
	}
}