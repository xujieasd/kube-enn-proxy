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