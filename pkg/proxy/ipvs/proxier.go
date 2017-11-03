package ipvs

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	//"syscall"
	"time"


	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/types"
	clientv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/api"
	"k8s.io/client-go/tools/record"

	"kube-enn-proxy/pkg/util/coreos/go-iptables/iptables"

	utilipvs "kube-enn-proxy/pkg/util/ipvs"
	//utiliptables "kube-enn-proxy/pkg/util/iptables"
	utilexec "kube-enn-proxy/pkg/util/exec"
	//utildbus "kube-enn-proxy/pkg/util/dbus"
	"kube-enn-proxy/pkg/proxy/util"
	"kube-enn-proxy/app/options"
	"kube-enn-proxy/pkg/watchers"

	"github.com/vishvananda/netlink"
)

//var ipvsInterface utilipvs.Interface

const (
	sysctlBase               = "/proc/sys"
	sysctlRouteLocalnet      = "net/ipv4/conf/all/route_localnet"
	sysctlBridgeCallIPTables = "net/bridge/bridge-nf-call-iptables"
	sysctlVSConnTrack        = "net/ipv4/vs/conntrack"
	sysctlForward            = "net/ipv4/ip_forward"
)

type Proxier struct {

	mu		    sync.Mutex
	serviceMap          util.ProxyServiceMap
	endpointsMap        util.ProxyEndpointMap
	portsMap            map[util.LocalPort]util.Closeable
	kernelIpvsMap       map[activeServiceKey][]activeServiceValue
	kernelClusterMap    map[string]int

	syncPeriod	    time.Duration
	minSyncPeriod       time.Duration


	masqueradeAll       bool
	exec                utilexec.Interface
	clusterCIDR         string
	hostname            string
	nodeIP              net.IP
	scheduler           string

	client              *kubernetes.Clientset

	ipvsInterface	    utilipvs.Interface
	//iptablesInterface   utiliptables.Interface
	recorder            record.EventRecorder
	portMapper          util.PortOpener

}


type activeServiceKey struct {
	ip       string
	port     int
	protocol string
}

type activeServiceValue struct {
	ip      string
	port    int
}

func NewProxier(
	clientset *kubernetes.Clientset,
	config *options.KubeEnnProxyConfig,
)(*Proxier, error){

	syncPeriod    := config.IpvsSyncPeriod
	minSyncPeriod := config.MinSyncPeriod

	// check valid user input
	if minSyncPeriod > syncPeriod {
		return nil, fmt.Errorf("min-sync (%v) must be < sync(%v)", minSyncPeriod, syncPeriod)
	}

	//protocol := utiliptables.ProtocolIpv4
	//if net.ParseIP(config.BindAddress).To4() == nil {
	//	protocol = utiliptables.ProtocolIpv6
	//}

	//var iptInterface utiliptables.Interface
	//var dbus utildbus.Interface

	execer := utilexec.New()
	ipvs := utilipvs.NewEnnIpvs()

	//dbus = utildbus.New()
	//iptInterface = utiliptables.New(execer, dbus, protocol)

	//err := ipvsInterface.InitIpvsInterface()
	glog.V(0).Infof("insmod ipvs module")

	err := setNetFlag()
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: setNetFlag fall: %v", err)
	}

	var masqueradeAll = false
	if config.MasqueradeAll {
		masqueradeAll = true
	}

	/*todo need to handle when CIDR not set*/
	clusterCIDR, err := util.GetPodCidrFromNodeSpec(clientset,config.HostnameOverride)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetPodCidr fall: %s", err.Error())
	}

	node, err := util.GetNode(clientset,config.HostnameOverride)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetNode fall: %s", err.Error())
	}
	hostname := node.Name

	nodeIP, err := util.InternalGetNodeHostIP(node)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetNodeIP fall: %s", err.Error())
	}
	var scheduler = utilipvs.DEFAULSCHE
	if len(config.IpvsScheduler) != 0{
		scheduler = config.IpvsScheduler
	}


	eventBroadcaster := record.NewBroadcaster()
	recorder := eventBroadcaster.NewRecorder(api.Scheme, clientv1.EventSource{Component: "kube-proxy", Host: hostname})

	IpvsProxier := Proxier{
		serviceMap:        make(util.ProxyServiceMap),
		endpointsMap:      make(util.ProxyEndpointMap),
		portsMap:          make(map[util.LocalPort]util.Closeable),
		kernelIpvsMap:     make(map[activeServiceKey][]activeServiceValue),
		kernelClusterMap:  make(map[string]int),
		masqueradeAll:     masqueradeAll,
		exec:              execer,
		clusterCIDR:       clusterCIDR,
		hostname:          hostname,
		nodeIP:            nodeIP,
		scheduler:         scheduler,
		syncPeriod:        syncPeriod,
		minSyncPeriod:     minSyncPeriod,
		client:            clientset,
		ipvsInterface:     ipvs,
		//iptablesInterface: iptInterface,
		recorder:          recorder,
		portMapper:        &util.ListenPortOpener{},
	}

	return &IpvsProxier, nil

}

func FakeProxier() *Proxier{
	ipvs := utilipvs.NewEnnIpvs()
	return &Proxier{
		ipvsInterface: ipvs,
	}
}

func (proxier *Proxier) SyncLoop(stopCh <-chan struct{}, wg *sync.WaitGroup){

	glog.V(2).Infof("Run IPVS Proxier")
	t := time.NewTicker(proxier.syncPeriod)
	defer t.Stop()
	defer wg.Done()

	glog.V(2).Infof("EnsureIPtablesMASQ: start")
	err := proxier.EnsureIPtablesMASQ()
	if err!= nil{
		glog.Errorf("SyncLoop: create MASQ failed: %s", err)
		return
	}

	for {
		select {
		case <-t.C:
			glog.V(2).Infof("Periodic sync")
			proxier.Sync()
		case <-stopCh:
			glog.V(2).Infof("Stop sync")
			return
		}
	}
}

func (proxier *Proxier) Sync(){

	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	err := proxier.EnsureIPtablesMASQ()
	if err!= nil{
		glog.Errorf("Sync: ennsure MASQ failed: %s", err)
		return
	}
	proxier.serviceMap, _ = util.BuildServiceMap(proxier.serviceMap)
	proxier.endpointsMap, _ = util.BuildEndPointsMap(proxier.hostname, proxier.endpointsMap)
	proxier.syncProxyRules()

}

func (proxier *Proxier) syncProxyRules(){

	start := time.Now()
	defer func() {
		glog.V(2).Infof("syncProxyRules took %v", time.Since(start))
	}()

	activeServiceMap := make(map[activeServiceKey][]activeServiceValue)

	/* create duumy link */
	dummylink, err := proxier.ipvsInterface.GetDummyLink()
	if err != nil{
		glog.Errorf("syncProxyRules: get dummy link feild: %s",err)
		return
	}

	// Accumulate the set of local ports that we will be holding open once this update is complete
	replacementPortsMap := map[util.LocalPort]util.Closeable{}
	proxier.BuildIpvsMap()
	proxier.BuildClusterMap(dummylink)

	glog.V(3).Infof("Syncing ipvs Proxier rules")

	for svcName, serviceInfo := range proxier.serviceMap {
		protocol := strings.ToLower(serviceInfo.Protocol)

		/* capture cluster ip */
		glog.V(6).Infof("  -- capture cluster ip ")
		ipvs_cluster_service := &utilipvs.Service{
			ClusterIP:       serviceInfo.ClusterIP,
			Port:            serviceInfo.Port,
			Protocol:        serviceInfo.Protocol,
			Scheduler:       utilipvs.DEFAULSCHE,
			SessionAffinity: serviceInfo.SessionAffinity,

		}

		/* add cluster ip to dummy link */

		_, ok := proxier.kernelClusterMap[ipvs_cluster_service.ClusterIP.String()]
		if !ok{
			glog.V(3).Infof("new cluter need to bind so add ip to dummy link: %s",ipvs_cluster_service.ClusterIP.String())
			err = proxier.ipvsInterface.AddDummyClusterIp(ipvs_cluster_service,dummylink)
			if err != nil{
				glog.Errorf("syncProxyRules: add dummy cluster ip feild: %s",err)
				continue
			}

		}else {
			glog.V(4).Infof("old cluter ip which already binded: %s",ipvs_cluster_service.ClusterIP.String())
		}



		clusterKey := activeServiceKey{
			ip:       ipvs_cluster_service.ClusterIP.String(),
			port:     ipvs_cluster_service.Port,
			protocol: ipvs_cluster_service.Protocol,
		}

		activeServiceMap[clusterKey] = make([]activeServiceValue,0)

		KernelEndpoints, hasCluster := proxier.kernelIpvsMap[clusterKey]
		if !hasCluster {
			glog.V(3).Infof("new cluter find so add cluster to ipvs")

			/* handle ipvs service add */
			err = proxier.ipvsInterface.AddIpvsService(ipvs_cluster_service)
			if err != nil{
				glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s",err)
				continue
			}

		}else {
			glog.V(4).Infof("old cluter ip %s:%d",ipvs_cluster_service.ClusterIP.String(),ipvs_cluster_service.Port)
		}

		/* handle ipvs destination add */
		for _, endpointinfo := range proxier.endpointsMap[svcName]{
			ipvs_server := &utilipvs.Server{
				Ip:      endpointinfo.Ip,
				Port:    endpointinfo.Port,
				Weight:  utilipvs.DEFAULWEIGHT,
			}

			endpointValue := activeServiceValue{
				ip:    ipvs_server.Ip,
				port:  ipvs_server.Port,
			}
			activeServiceMap[clusterKey] = append(activeServiceMap[clusterKey],endpointValue)

			isEndpointActive := false
			if hasCluster{
				// cluster ip is in ipvs rule, should check whether dst is in rule
				for _, oldDst := range KernelEndpoints {
					glog.V(6).Infof("old dst %s:%d", oldDst.ip, oldDst.port)
					if strings.Compare(endpointinfo.Ip, oldDst.ip) == 0 && endpointinfo.Port == oldDst.port {
						//endpoint is in ipvs rule
						isEndpointActive = true
						glog.V(6).Infof("endpoint is in ipvs rule so skip add endpoint")
						break
					}
				}

			}
			if isEndpointActive{
				continue
			}
			err = proxier.ipvsInterface.AddIpvsServer(ipvs_cluster_service, ipvs_server)
			if err != nil{
				glog.Errorf("syncProxyRules: add ipvs destination feild: %s",err)
				continue
			}



		}


		/* Capture externalIPs */
		glog.V(6).Infof("  -- capture external ip ")
		for _, externalIP := range serviceInfo.ExternalIPs {
			// If the "external" IP happens to be an IP that is local to this
			// machine, hold the local port open so no other process can open it
			// (because the socket might open but it would never work).
			if local, err := isLocalIP(externalIP); err != nil {
				glog.Errorf("can't determine if IP is local, assuming not: %v", err)
			} else if local {
				lp := util.LocalPort{
					Desc:     "externalIP for " + svcName.String(),
					Ip:       externalIP,
					Port:     serviceInfo.Port,
					Protocol: protocol,
				}
				if proxier.portsMap[lp] != nil {
					glog.V(4).Infof("Port %s was open before and is still needed", lp.String())
					replacementPortsMap[lp] = proxier.portsMap[lp]
				} else {
					socket, err := proxier.portMapper.OpenLocalPort(&lp)
					if err != nil {
						msg := fmt.Sprintf("can't open %s, skipping this externalIP: %v", lp.String(), err)

						proxier.recorder.Eventf(
							&clientv1.ObjectReference{
								Kind:      "Node",
								Name:      proxier.hostname,
								UID:       types.UID(proxier.hostname),
								Namespace: "",
							}, api.EventTypeWarning, err.Error(), msg)
						glog.Error(msg)
						continue
					}
					replacementPortsMap[lp] = socket
				}
			} // We're holding the port, so it's OK to install ipvs rules.

			ip := net.ParseIP(externalIP)
			ipvs_externalIp_service := &utilipvs.Service{
				ClusterIP:       ip,
				Port:            serviceInfo.Port,
				Protocol:        serviceInfo.Protocol,
				Scheduler:       utilipvs.DEFAULSCHE,
				SessionAffinity: serviceInfo.SessionAffinity,

			}

			externalIPKey := activeServiceKey{
				ip:       ipvs_externalIp_service.ClusterIP.String(),
				port:     ipvs_externalIp_service.Port,
				protocol: ipvs_externalIp_service.Protocol,
			}
			activeServiceMap[externalIPKey] = make([]activeServiceValue,0)


			KernelEndpoints, hasCluster := proxier.kernelIpvsMap[externalIPKey]
			if !hasCluster {
				glog.V(3).Infof("new cluter find so add cluster to ipvs")

				/* handle ipvs service add */
				err = proxier.ipvsInterface.AddIpvsService(ipvs_externalIp_service)
				if err != nil{
					glog.Errorf("syncProxyRules: add ipvs node service feild: %s",err)
					continue
				}

			}else {
				glog.V(3).Infof("old cluter ip %s:%d",ipvs_externalIp_service.ClusterIP.String(),ipvs_externalIp_service.Port)
			}


			/* handle ipvs destination add */
			for _, endpointinfo := range proxier.endpointsMap[svcName]{
				ipvs_server := &utilipvs.Server{
					Ip:      endpointinfo.Ip,
					Port:    endpointinfo.Port,
					Weight:  utilipvs.DEFAULWEIGHT,
				}

				endpointValue := activeServiceValue{
					ip:    ipvs_server.Ip,
					port:  ipvs_server.Port,
				}
				activeServiceMap[externalIPKey] = append(activeServiceMap[externalIPKey],endpointValue)

				isEndpointActive := false
				if hasCluster{
					// cluster ip is in ipvs rule, should check whether dst is in rule
					for _, oldDst := range KernelEndpoints {
						glog.V(6).Infof("old dst %s:%d", oldDst.ip, oldDst.port)
						if strings.Compare(endpointinfo.Ip, oldDst.ip) == 0 && endpointinfo.Port == oldDst.port {
							//endpoint is in ipvs rule
							isEndpointActive = true
							glog.V(6).Infof("endpoint is in ipvs rule so skip add endpoint")
							break
						}
					}

				}
				if isEndpointActive{
					continue
				}

				err = proxier.ipvsInterface.AddIpvsServer(ipvs_externalIp_service, ipvs_server)
				if err != nil {
					glog.Errorf("syncProxyRules: add ipvs destination feild: %s", err)
					continue

				}

			}
		}



		/* capture node port */
		glog.V(6).Infof("  -- capture node port ")
		if serviceInfo.NodePort != 0{

			// Hold the local port open so no other process can open it
			// (because the socket might open but it would never work).
			lp := util.LocalPort{
				Desc:     "nodePort for " + svcName.String(),
				Ip:       "",
				Port:     serviceInfo.NodePort,
				Protocol: protocol,
			}
			if proxier.portsMap[lp] != nil {
				glog.V(4).Infof("Port %s was open before and is still needed", lp.String())
				replacementPortsMap[lp] = proxier.portsMap[lp]
			} else {
				socket, err := proxier.portMapper.OpenLocalPort(&lp)
				if err != nil {
					glog.Errorf("can't open %s, skipping this nodePort: %v", lp.String(), err)
					continue
				}
				if lp.Protocol == "udp" {
					util.ClearUdpConntrackForPort(proxier.exec, lp.Port)
				}
				replacementPortsMap[lp] = socket
			} // We're holding the port, so it's OK to install ipvs rules.

			ipvs_node_service := &utilipvs.Service{
				ClusterIP:        proxier.nodeIP,
				Port:             serviceInfo.NodePort,
				Protocol:         serviceInfo.Protocol,
				Scheduler:        utilipvs.DEFAULSCHE,
				SessionAffinity:  serviceInfo.SessionAffinity,
			}

			nodeKey := activeServiceKey{
				ip:       ipvs_node_service.ClusterIP.String(),
				port:     ipvs_node_service.Port,
				protocol: ipvs_node_service.Protocol,
			}
			activeServiceMap[nodeKey] = make([]activeServiceValue,0)

			KernelEndpoints, hasCluster := proxier.kernelIpvsMap[nodeKey]
			if !hasCluster {
				glog.V(3).Infof("new cluter find so add cluster to ipvs")

				/* handle ipvs service add */
				err = proxier.ipvsInterface.AddIpvsService(ipvs_node_service)
				if err != nil{
					glog.Errorf("syncProxyRules: add ipvs node service feild: %s",err)
					continue
				}

			}else {
				glog.V(3).Infof("old cluter ip %s:%d",ipvs_node_service.ClusterIP.String(),ipvs_node_service.Port)
			}

			/* handle ipvs destination add */
			for _, endpointinfo := range proxier.endpointsMap[svcName]{
				ipvs_server := &utilipvs.Server{
					Ip:      endpointinfo.Ip,
					Port:    endpointinfo.Port,
					Weight:  utilipvs.DEFAULWEIGHT,
				}

				endpointValue := activeServiceValue{
					ip:    ipvs_server.Ip,
					port:  ipvs_server.Port,
				}
				activeServiceMap[nodeKey] = append(activeServiceMap[nodeKey],endpointValue)

				isEndpointActive := false
				if hasCluster{
					// cluster ip is in ipvs rule, should check whether dst is in rule
					for _, oldDst := range KernelEndpoints {
						glog.V(6).Infof("old dst %s:%d", oldDst.ip, oldDst.port)
						if strings.Compare(endpointinfo.Ip, oldDst.ip) == 0 && endpointinfo.Port == oldDst.port {
							//endpoint is in ipvs rule
							isEndpointActive = true
							glog.V(6).Infof("endpoint is in ipvs rule so skip add endpoint")
							break
						}
					}

				}
				if isEndpointActive{
					continue
				}

				err = proxier.ipvsInterface.AddIpvsServer(ipvs_node_service, ipvs_server)
				if err != nil{
					glog.Errorf("syncProxyRules: add ipvs destination feild: %s",err)
					continue

				}

			}
		}


	}
	glog.V(4).Infof("create ipvs rule took %v", time.Since(start))

	/* compare active ipvs info with old ipvs info
	   if there is any rule remain in the old ipvs info
	   but doesn't exist in the active ipvs info
	   then remove the rule
		*/
	oldSvcs, err := proxier.ipvsInterface.ListIpvsService()
	if err != nil{
		panic(err)
	}
	for _, oldSvc := range oldSvcs{
		/*
		serviceKey := activeServiceKey{
			ip:       oldSvc.ClusterIP.String(),
			port:     oldSvc.Port,
			protocol: oldSvc.Protocol,
		}
		*/
		oldSvc_t, err := utilipvs.CreateInterService(oldSvc)
		serviceKey := activeServiceKey{
			ip:       oldSvc_t.ClusterIP.String(),
			port:     oldSvc_t.Port,
			protocol: oldSvc_t.Protocol,
		}
		glog.V(4).Infof("check active service: %s:%d:%s",serviceKey.ip,serviceKey.port,serviceKey.protocol)
		activeEndpoints, ok := activeServiceMap[serviceKey]

		/* unused service info so remove ipvs config and dummy cluster ip*/
		if !ok{
			glog.V(2).Infof("delete unused ipvs service config and dummy cluster ip: %s:%d:%s",serviceKey.ip,serviceKey.port,serviceKey.protocol)
			//err = proxier.ipvsInterface.DeleteIpvsService(oldSvc)
			err = proxier.ipvsInterface.DeleteIpvsService(oldSvc_t)

			if err != nil{
				glog.Errorf("clean unused ipvs service failed: %s",err)
				continue
			}
			//if strings.Compare(oldSvc.ClusterIP.String(),proxier.nodeIP.String()) != 0{
			if strings.Compare(oldSvc_t.ClusterIP.String(),proxier.nodeIP.String()) != 0{
				//err = proxier.ipvsInterface.DeleteDummyClusterIp(oldSvc,dummylink)
				err = proxier.ipvsInterface.DeleteDummyClusterIp(oldSvc_t,dummylink)

				if err != nil{
					glog.Errorf("clean unused dummy cluster ip failed: %s",err)
					continue
				}
			}

		} else {
			/* check unused dst info so remove ipvs dst config */
			glog.V(4).Infof("check unused dst info so remove ipvs dst config")
			oldDsts, err := proxier.ipvsInterface.ListIpvsServer(oldSvc)
			if err != nil{
				panic(err)
			}
			for _, oldDst := range oldDsts{
				glog.V(4).Infof("old dst %s:%d", oldDst.Ip,oldDst.Port)
				isActive := false
				for _, activeEP := range activeEndpoints{
					glog.V(4).Infof("active endpoints %s:%d", activeEP.ip, activeEP.port)
					if strings.Compare(activeEP.ip,oldDst.Ip) == 0 && activeEP.port == oldDst.Port{
						isActive = true
						glog.Infof("endpoints %s:%d still active",activeEP.ip,activeEP.port)
						break
					}
				}
				if !isActive{
					glog.V(2).Infof("delete unused ipvs destination %s:%d from %s:%d", oldDst.Ip, oldDst.Port, serviceKey.ip,serviceKey.port)
					//err = proxier.ipvsInterface.DeleteIpvsServer(oldSvc,oldDst)
					err = proxier.ipvsInterface.DeleteIpvsServer(oldSvc_t,oldDst)
					if err != nil{
						glog.Errorf("clean unused ipvs destination config failed: %s",err)
						continue
					}
				}
			}

		}
	}
	glog.V(4).Infof("check ipvs rule took %v", time.Since(start))

	// Close old local ports and save new ones.
	for k, v := range proxier.portsMap {
		if replacementPortsMap[k] == nil {
			v.Close()
		}
	}
	proxier.portsMap = replacementPortsMap

}

func (proxier *Proxier) BuildIpvsMap() {

	ipvsMap := make(map[activeServiceKey][]activeServiceValue)

	ipvsSvcs, err := proxier.ipvsInterface.ListIpvsService()
	if err != nil{
		panic(err)
	}

	for _, ipvsSvc := range ipvsSvcs{

		ipvsSvc_t, err := utilipvs.CreateInterService(ipvsSvc)
		if err != nil{
			glog.Errorf("BuildIpvsMap: InterService %s:%d create fail %v \n",ipvsSvc.Address.String(),int(ipvsSvc.Port),err)
			continue
		}
		serviceKey := activeServiceKey{
			ip:       ipvsSvc_t.ClusterIP.String(),
			port:     ipvsSvc_t.Port,
			protocol: ipvsSvc_t.Protocol,
		}
		ipvsMap[serviceKey] = make([]activeServiceValue,0)

		ipvsDsts, err := proxier.ipvsInterface.ListIpvsServer(ipvsSvc)

		if err != nil{
			glog.Errorf("BuildIpvsMap: list destination of Service %s:%d fail %v \n",ipvsSvc.Address.String(),int(ipvsSvc.Port),err)
			continue
		}
		for _, ipvsDst := range ipvsDsts {
			endpointValue := activeServiceValue{
				ip:    ipvsDst.Ip,
				port:  ipvsDst.Port,
			}

			ipvsMap[serviceKey] = append(ipvsMap[serviceKey],endpointValue)
		}
	}

	proxier.kernelIpvsMap = ipvsMap

}

func (proxier *Proxier) BuildClusterMap(dummylink netlink.Link) {

	clusterMap := make(map[string]int)
	addrs, _ := proxier.ipvsInterface.ListDuumyClusterIp(dummylink)

	for _, addr := range addrs{
		ipKey := addr.IP.String()
		clusterMap[ipKey] = 1
	}
	proxier.kernelClusterMap = clusterMap

}

func isLocalIP(ip string) (bool, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false, err
	}
	for i := range addrs {
		intf, _, err := net.ParseCIDR(addrs[i].String())
		if err != nil {
			return false, err
		}
		if net.ParseIP(ip).Equal(intf) {
			return true, nil
		}
	}
	return false, nil
}

func (proxier *Proxier) OnEndpointsUpdate(endpointsUpdate *watchers.EndpointsUpdate){
	proxier.mu.Lock()
	defer proxier.mu.Unlock()

	if !(watchers.ServiceWatchConfig.HasSynced() && watchers.EndpointsWatchConfig.HasSynced()) {
		glog.V(2).Infof("Skipping ipvs server sync because local cache is not synced yet")
	}

	newEndpointsMap, staleConnections := util.BuildEndPointsMap(proxier.hostname, proxier.endpointsMap)

	if len(newEndpointsMap) != len(proxier.endpointsMap) || !reflect.DeepEqual(newEndpointsMap, proxier.endpointsMap) {
		proxier.endpointsMap = newEndpointsMap
		proxier.syncProxyRules()
	} else {
		glog.V(2).Infof("Skipping proxy ipvs rule sync on endpoint update because nothing changed")
	}
	util.DeleteEndpointConnections(proxier.exec, proxier.serviceMap, staleConnections)
}

func (proxier *Proxier) OnServicesUpdate(servicesUpdate *watchers.ServicesUpdate){
	proxier.mu.Lock()
	defer proxier.mu.Unlock()

	if !(watchers.ServiceWatchConfig.HasSynced() && watchers.EndpointsWatchConfig.HasSynced()) {
		glog.V(2).Infof("Skipping ipvs server sync because local cache is not synced yet")
	}

	newServiceMap, staleUDPServices := util.BuildServiceMap(proxier.serviceMap)

	if len(newServiceMap) != len(proxier.serviceMap) || !reflect.DeepEqual(newServiceMap, proxier.serviceMap) {
		proxier.serviceMap = newServiceMap
		proxier.syncProxyRules()
	} else {
		glog.V(2).Infof("Skipping proxy ipvs rule sync on service update because nothing changed")
	}
	util.DeleteServiceConnections(proxier.exec, staleUDPServices.List())
}


func CanUseIpvs() (bool, error){

	/*todo: check whether IPVS can be used in nodes*/


	return true, nil
}

func (proxier *Proxier) EnsureIPtablesMASQ() error{

	glog.V(4).Infof("ensure ipvs masq rule\n")
	iptablehandler, err := iptables.New()
	if err != nil {
		return errors.New("EnsureIPtablesMASQ: iptable init failed" + err.Error())
	}
	/*
	IPVS match options:
	[!] --ipvs                      packet belongs to an IPVS connection

	Any of the following options implies --ipvs (even negated)
	[!] --vproto protocol           VIP protocol to match; by number or name,
                        	        e.g. "tcp"
	[!] --vaddr address[/mask]      VIP address to match
	[!] --vport port                VIP port to match; by number or name,
                	                e.g. "http"
    	--vdir {ORIGINAL|REPLY}     flow direction of packet
	[!] --vmethod {GATE|IPIP|MASQ}  IPVS forwarding method used
	[!] --vportctl port             VIP port of the controlling connection to
        	                        match, e.g. 21 for FTP
	 */

	var args []string

	if proxier.masqueradeAll{
		args = []string{
			"-m", "ipvs", "--ipvs", "--vdir", "ORIGINAL", "--vmethod", "MASQ",
			"-m", "comment", "--comment", "IPVS SNAT MASQ ALL",
			"-j", "MASQUERADE",
		}
		err = iptablehandler.PrependUnique("nat", "POSTROUTING", args...)
		if err != nil {
			return errors.New("EnsureIPtablesMASQ: iptables masq all failed" + err.Error())
		}
		//if _, err := proxier.iptablesInterface.EnsureRule(
		//	utiliptables.Prepend,
		//	utiliptables.TableNAT,
		//	utiliptables.ChainPostrouting,
		//	args...,
		//); err != nil {
		//	return fmt.Errorf("Failed to ensure ipvs masq that %s chain %s: %v", utiliptables.TableNAT, utiliptables.ChainPostrouting, err)
		//}
	}
	if len(proxier.clusterCIDR) > 0 {
		args = []string{
			"!", "-s", proxier.clusterCIDR,
			"-m", "ipvs", "--ipvs", "--vdir", "ORIGINAL", "--vmethod", "MASQ",
			"-m", "comment", "--comment", "IPVS SNAT",
			"-j", "MASQUERADE",
		}
		err = iptablehandler.PrependUnique("nat", "POSTROUTING", args...)
		if err != nil {
			return errors.New("EnsureIPtablesMASQ: iptables masq all failed" + err.Error())
		}
		//if _, err := proxier.iptablesInterface.EnsureRule(
		//	utiliptables.Prepend,
		//	utiliptables.TableNAT,
		//	utiliptables.ChainPostrouting,
		//	args...,
		//); err != nil {
		//	return fmt.Errorf("Failed to ensure ipvs masq that %s chain %s: %v", utiliptables.TableNAT, utiliptables.ChainPostrouting, err)
		//}
	}

	return nil
}

func (proxier *Proxier) CleanupLeftovers(){
	proxier.cleanUpIptablesMASQ()
	proxier.cleanUpIpvs()
	proxier.cleanUpDummyLink()
}

func (proxier *Proxier) cleanUpIptablesMASQ() error{
	glog.V(0).Infof("cleanUpIPtablesMASQ")
	iptablehandler, err := iptables.New()
	if err != nil {
		return errors.New("cleanUpIptablesMASQ: iptable init failed" + err.Error())
	}

	post_routing_list, err := iptablehandler.List("nat", "POSTROUTING")
	if err != nil {
		return errors.New("cleanUpIptablesMASQ: list post routing failed" + err.Error())
	}

	for i, post := range post_routing_list{
		if strings.Contains(post,"ipvs") && strings.Contains(post,"MASQUERADE"){
			arg := strconv.Itoa(i)
			err = iptablehandler.Delete("nat","POSTROUTING", arg)
			if err != nil {
				return errors.New("cleanUpIptablesMASQ: delete post routing failed" + err.Error())
			}
		}
	}

	return nil
}

func (proxier *Proxier) cleanUpIpvs(){
	glog.V(0).Infof("cleanUpIpvs")
	proxier.ipvsInterface.FlushIpvs()
}

func (proxier *Proxier) cleanUpDummyLink(){
	glog.V(0).Infof("cleanUpDummyLink")
	/* dummy cluster ip will clean up together with dummy link*/
	proxier.ipvsInterface.DeleteDummyLink()
}

func setNetFlag() error{
	if err := setSysctl(sysctlRouteLocalnet, 1); err != nil {
		return fmt.Errorf("can't set sysctl %s: %v", sysctlRouteLocalnet, err)
	}

	if val, err := getSysctl(sysctlBridgeCallIPTables); err == nil && val != 1 {
		glog.V(0).Infof("missing br-netfilter module or unset sysctl br-nf-call-iptables; proxy may not work as intended")
	}

	if err := setSysctl(sysctlVSConnTrack, 1); err != nil {
		return fmt.Errorf("can't set sysctl %s: %v", sysctlVSConnTrack, err)
	}
/*
	if err := sysctl.SetSysctl(sysctlForward, 1); err != nil {
		return fmt.Errorf("can't set sysctl %s: %v", sysctlForward, err)
	}
*/
	return nil
}

func setSysctl(sysctl string, newVal int) error {
	return ioutil.WriteFile(path.Join(sysctlBase, sysctl), []byte(strconv.Itoa(newVal)), 0640)
}

func getSysctl(sysctl string) (int, error) {
	data, err := ioutil.ReadFile(path.Join(sysctlBase, sysctl))
	if err != nil {
		return -1, err
	}
	val, err := strconv.Atoi(strings.Trim(string(data), " \n"))
	if err != nil {
		return -1, err
	}
	return val, nil
}
