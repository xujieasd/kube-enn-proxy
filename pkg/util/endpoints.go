package util

import (

	"kube-enn-proxy/pkg/proxy"
	"k8s.io/apimachinery/pkg/types"
	"kube-enn-proxy/pkg/watchers"
	api "k8s.io/client-go/pkg/api/v1"

	"github.com/golang/glog"

)

type EndpointsInfo struct {
	Ip       string
	Port     int
	IsLocal  bool
}

type ProxyEndpointMap map[proxy.ServicePortName][]*EndpointsInfo


func BuildEndPointsMap(hostname string) ProxyEndpointMap{
	glog.Infof("BuildEndPointMap")
	endpointsMap := make(ProxyEndpointMap)

	for _, endpoints := range watchers.EndpointsWatchConfig.List() {

		svcName := types.NamespacedName{
			Namespace: endpoints.Namespace,
			Name:      endpoints.Name,
		}

		for _, endpoints_sub := range endpoints.Subsets{
			for _, ports := range endpoints_sub.Ports{
				if ports.Port == 0 {
					glog.Warningf("ignoring invalid endpoint port %s", ports.Name)
					continue
				}

				serviceName := proxy.ServicePortName{
					NamespacedName: svcName,
					Port:           ports.Name,
				}

				new_endpoints := make([]*EndpointsInfo, 0)

				for _, addr := range endpoints_sub.Addresses{
					if addr.IP == "" {
						glog.Warningf("ignoring invalid endpoint port %s with empty host", ports.Name)
						continue
					}
					info := newEndpointsInfo(addr,ports,hostname)

					new_endpoints = append(new_endpoints, info)
				}
				endpointsMap[serviceName] = new_endpoints
			}
		}
	}


	return endpointsMap
}

func newEndpointsInfo(address api.EndpointAddress, port api.EndpointPort, hostname string) *EndpointsInfo {

	info := &EndpointsInfo{
		Ip:         address.IP,
		Port:       int(port.Port),
		IsLocal:    address.NodeName != nil && *address.NodeName == hostname,
	}

	return info
}
