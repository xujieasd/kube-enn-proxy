package util

import (
	"net"
	"strings"
	"kube-enn-proxy/pkg/proxy"
	"k8s.io/apimachinery/pkg/types"
	"kube-enn-proxy/pkg/watchers"
	api "k8s.io/client-go/pkg/api/v1"

	"github.com/golang/glog"

	"k8s.io/apimachinery/pkg/util/sets"
)

type ServiceInfo struct {
	ClusterIP           net.IP
	Port                int
	Protocol            string
	NodePort            int
	SessionAffinity     bool
	ExternalIPs         []string

}

type ProxyServiceMap map[proxy.ServicePortName]*ServiceInfo

func BuildServiceMap(oldServiceMap ProxyServiceMap) (ProxyServiceMap, sets.String){

	glog.V(2).Infof("BuildServiceMap")
	newServiceMap := make(ProxyServiceMap)

	for _, service := range watchers.ServiceWatchConfig.List(){

		svcName := types.NamespacedName{
			Namespace: service.Namespace,
			Name:      service.Name,
		}

		// if ClusterIP is "None" or empty, skip proxying
		if service.Spec.ClusterIP == "None" || service.Spec.ClusterIP == "" {
			glog.V(2).Infof("Skipping service %s due to clusterIP is null", svcName)
			continue
		}
		// Even if ClusterIP is set, ServiceTypeExternalName services don't get proxied
		if service.Spec.Type == "ExternalName" {
			glog.V(2).Infof("Skipping service %s due to Type=ExternalName", svcName)
			continue
		}

		for i := range service.Spec.Ports {
			servicePort := &service.Spec.Ports[i]

			serviceName := proxy.ServicePortName{
				NamespacedName: svcName,
				Port:           servicePort.Name,
			}

			info := newServiceInfo(serviceName, servicePort, service)

			newServiceMap[serviceName] = info
		}

	}

	staleUDPServices := sets.NewString()
	// Remove serviceports missing from the update.
	for name, info := range oldServiceMap {
		if _, exists := newServiceMap[name]; !exists {
			glog.V(2).Infof("Removing service %q", name)
			if info.Protocol == strings.ToLower(string(api.ProtocolUDP)){
				staleUDPServices.Insert(info.ClusterIP.String())
			}
		}
	}

	return newServiceMap, staleUDPServices

}

func newServiceInfo(serviceName proxy.ServicePortName, port *api.ServicePort, service *api.Service) *ServiceInfo{

	info := &ServiceInfo{
		ClusterIP:                net.ParseIP(service.Spec.ClusterIP),
		Port:                     int(port.Port),
		Protocol:                 strings.ToLower(string(port.Protocol)),
		NodePort:                 int(port.NodePort),
		ExternalIPs:              make([]string, len(service.Spec.ExternalIPs)),
	}
	copy(info.ExternalIPs, service.Spec.ExternalIPs)

	if service.Spec.SessionAffinity == "ClientIP"{
		info.SessionAffinity = true
	} else {
		info.SessionAffinity = false
	}

	return info

}