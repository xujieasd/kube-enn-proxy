package util

import (

	"kube-enn-proxy/pkg/proxy"
	"k8s.io/apimachinery/pkg/types"
	"kube-enn-proxy/pkg/watchers"
	api "k8s.io/client-go/pkg/api/v1"

	"github.com/golang/glog"

	"net"
	"strconv"
)

type EndpointsInfo struct {
	Ip       string
	Port     int
	IsLocal  bool
}

type ProxyEndpointMap map[proxy.ServicePortName][]*EndpointsInfo


type EndpointServicePair struct {
	Endpoint        string
	ServicePortName proxy.ServicePortName
}

func BuildEndPointsMap(hostname string, curMap ProxyEndpointMap) (ProxyEndpointMap, map[EndpointServicePair]bool){
	glog.Infof("BuildEndPointMap")
	endpointsMap := make(ProxyEndpointMap)
	staleSet := make(map[EndpointServicePair]bool)

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

	// Check stale connections against endpoints missing from the update.
	// TODO: we should really only mark a connection stale if the proto was UDP
	// and the (ip, port, proto) was removed from the endpoints.
	for svcPort, epList := range curMap {
		for _, ep := range epList {
			stale := true

			for i := range endpointsMap[svcPort] {
				if *endpointsMap[svcPort][i] == *ep {
					stale = false
					break
				}
			}
			if stale {
				endpoint := net.JoinHostPort(ep.Ip,strconv.Itoa(ep.Port))
				glog.Infof("Stale endpoint %v -> %v", svcPort, endpoint)
				staleSet[EndpointServicePair{Endpoint: endpoint, ServicePortName: svcPort}] = true
			}
		}
	}

	return endpointsMap, staleSet
}

func newEndpointsInfo(address api.EndpointAddress, port api.EndpointPort, hostname string) *EndpointsInfo {

	info := &EndpointsInfo{
		Ip:         address.IP,
		Port:       int(port.Port),
		IsLocal:    address.NodeName != nil && *address.NodeName == hostname,
	}

	return info
}
