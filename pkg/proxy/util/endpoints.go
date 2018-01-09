package util

import (
	"kube-enn-proxy/pkg/proxy"
	"k8s.io/apimachinery/pkg/types"
	"kube-enn-proxy/pkg/watchers"
	api "k8s.io/api/core/v1"
	"github.com/golang/glog"
	//"k8s.io/apimachinery/pkg/util/sets"

	"net"
	"strconv"
	"sync"
	"reflect"
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

type UpdateEndpointMapResult struct {
	HcEndpoints       map[types.NamespacedName]int
	StaleEndpoints    map[EndpointServicePair]bool
	StaleServiceNames map[proxy.ServicePortName]bool
}

type EndpointsChange struct {
	Previous ProxyEndpointMap
	Current  ProxyEndpointMap
}

type EndpointsChangeMap struct {
	Lock     sync.Mutex
	Hostname string
	Items    map[types.NamespacedName]*EndpointsChange
}

func BuildEndPointsMap(hostname string, curMap ProxyEndpointMap) (ProxyEndpointMap, map[EndpointServicePair]bool){
	glog.V(3).Infof("BuildEndPointMap")
	endpointsMap := make(ProxyEndpointMap)
	//hcEndpoints := make(map[types.NamespacedName]int)
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
					info := NewEndpointsInfo(addr,ports,hostname)

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
				glog.V(3).Infof("Stale endpoint %v -> %v", svcPort, endpoint)
				staleSet[EndpointServicePair{Endpoint: endpoint, ServicePortName: svcPort}] = true
			}
		}
	}


	//localIPs := map[types.NamespacedName]sets.String{}
	//for svcPort := range endpointsMap {
	//	for _, ep := range endpointsMap[svcPort] {
	//		if ep.IsLocal {
	//			nsn := svcPort.NamespacedName
	//			if localIPs[nsn] == nil {
	//				localIPs[nsn] = sets.NewString()
	//			}
	//			ip := ep.Ip
	//			localIPs[nsn].Insert(ip)
	//		}
	//	}
	//}
	// produce a count per service
	//for nsn, ips := range localIPs {
	//	hcEndpoints[nsn] = len(ips)
	//}

	return endpointsMap, staleSet
}

func NewEndpointsInfo(address api.EndpointAddress, port api.EndpointPort, hostname string) *EndpointsInfo {

	info := &EndpointsInfo{
		Ip:         address.IP,
		Port:       int(port.Port),
		IsLocal:    address.NodeName != nil && *address.NodeName == hostname,
	}

	return info
}

func NewEndpointsChangeMap(hostname string) EndpointsChangeMap {
	return EndpointsChangeMap{
		Hostname: hostname,
		Items:    make(map[types.NamespacedName]*EndpointsChange),
	}
}

func (ecm *EndpointsChangeMap) Update(namespacedName *types.NamespacedName, previous, current *api.Endpoints) bool {
	ecm.Lock.Lock()
	defer ecm.Lock.Unlock()

	change, exists := ecm.Items[*namespacedName]
	if !exists {
		change = &EndpointsChange{}
		change.Previous = endpointsToEndpointsMap(previous, ecm.Hostname)
		ecm.Items[*namespacedName] = change
	}
	change.Current = endpointsToEndpointsMap(current, ecm.Hostname)
	if reflect.DeepEqual(change.Previous, change.Current) {
		delete(ecm.Items, *namespacedName)
	}
	return len(ecm.Items) > 0
}

// Translates single Endpoints object to proxyEndpointsMap.
// This function is used for incremental updated of endpointsMap.
//
// NOTE: endpoints object should NOT be modified.
func endpointsToEndpointsMap(endpoints *api.Endpoints, hostname string) ProxyEndpointMap {
	if endpoints == nil {
		return nil
	}

	endpointsMap := make(ProxyEndpointMap)
	// We need to build a map of portname -> all ip:ports for that
	// portname.  Explode Endpoints.Subsets[*] into this structure.
	for i := range endpoints.Subsets {
		ss := &endpoints.Subsets[i]
		for i := range ss.Ports {
			port := &ss.Ports[i]
			if port.Port == 0 {
				glog.Warningf("ignoring invalid endpoint port %s", port.Name)
				continue
			}
			svcPort := proxy.ServicePortName{
				NamespacedName: types.NamespacedName{Namespace: endpoints.Namespace, Name: endpoints.Name},
				Port:           port.Name,
			}
			for i := range ss.Addresses {
				addr := &ss.Addresses[i]
				if addr.IP == "" {
					glog.Warningf("ignoring invalid endpoint port %s with empty host", port.Name)
					continue
				}
				epInfo := &EndpointsInfo{
					Ip:      addr.IP,
					Port:    int(port.Port),
					IsLocal: addr.NodeName != nil && *addr.NodeName == hostname,
				}
				endpointsMap[svcPort] = append(endpointsMap[svcPort], epInfo)
			}
			if glog.V(3) {
				newEPList := []string{}
				for _, ep := range endpointsMap[svcPort] {
					newEPList = append(newEPList, net.JoinHostPort(ep.Ip, strconv.Itoa(ep.Port)))
				}
				glog.Infof("Setting endpoints for %q to %+v", svcPort, newEPList)
			}
		}
	}
	return endpointsMap
}

// <endpointsMap> is updated by this function (based on the given changes).
// <changes> map is cleared after applying them.
func UpdateEndpointsMap(endpointsMap ProxyEndpointMap, changes *EndpointsChangeMap, hostname string) (result UpdateEndpointMapResult) {
	result.StaleEndpoints = make(map[EndpointServicePair]bool)
	result.StaleServiceNames = make(map[proxy.ServicePortName]bool)

	func() {
		changes.Lock.Lock()
		defer changes.Lock.Unlock()
		for _, change := range changes.Items {
			endpointsMap.unmerge(change.Previous)
			endpointsMap.merge(change.Current)
			detectStaleConnections(change.Previous, change.Current, result.StaleEndpoints, result.StaleServiceNames)
		}
		//changes.Items = make(map[types.NamespacedName]*EndpointsChange)
	}()

//	if !utilfeature.DefaultFeatureGate.Enabled(features.ExternalTrafficLocalOnly) {
//		return
//	}

	// TODO: If this will appear to be computationally expensive, consider
	// computing this incrementally similarly to endpointsMap.
//	result.hcEndpoints = make(map[types.NamespacedName]int)
//	localIPs := getLocalIPs(endpointsMap)
//	for nsn, ips := range localIPs {
//		result.hcEndpoints[nsn] = len(ips)
//	}

	return result
}


// <staleEndpoints> and <staleServices> are modified by this function with detected stale connections.
func detectStaleConnections(oldEndpointsMap, newEndpointsMap ProxyEndpointMap, staleEndpoints map[EndpointServicePair]bool, staleServiceNames map[proxy.ServicePortName]bool) {
	for svcPortName, epList := range oldEndpointsMap {
		for _, ep := range epList {
			stale := true
			for i := range newEndpointsMap[svcPortName] {
				if *newEndpointsMap[svcPortName][i] == *ep {
					stale = false
					break
				}
			}
			if stale {
				endpoint := net.JoinHostPort(ep.Ip,strconv.Itoa(ep.Port))
				glog.V(4).Infof("Stale endpoint %v -> %v", svcPortName, endpoint)
				staleEndpoints[EndpointServicePair{Endpoint: endpoint, ServicePortName: svcPortName}] = true
			}
		}
	}

	for svcPortName, epList := range newEndpointsMap {
		// For udp service, if its backend changes from 0 to non-0. There may exist a conntrack entry that could blackhole traffic to the service.
		if len(epList) > 0 && len(oldEndpointsMap[svcPortName]) == 0 {
			staleServiceNames[svcPortName] = true
		}
	}
}

func (em ProxyEndpointMap) merge(other ProxyEndpointMap) {
	for svcPort := range other {
		em[svcPort] = other[svcPort]
	}
}

func (em ProxyEndpointMap) unmerge(other ProxyEndpointMap) {
	for svcPort := range other {
		delete(em, svcPort)
	}
}

