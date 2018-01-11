package ipvs

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"sync/atomic"

	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"
	"k8s.io/apimachinery/pkg/types"
	//"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	//"k8s.io/apimachinery/pkg/util/wait"
	api "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"

	"kube-enn-proxy/pkg/util/coreos/go-iptables/iptables"

	utilipvs "kube-enn-proxy/pkg/util/ipvs"
	//utiliptables "kube-enn-proxy/pkg/util/iptables"
	utilexec "k8s.io/utils/exec"
	//utildbus "kube-enn-proxy/pkg/util/dbus"
	"kube-enn-proxy/pkg/proxy/util"
	"kube-enn-proxy/app/options"
	//"kube-enn-proxy/pkg/watchers"
	"kube-enn-proxy/pkg/proxy/healthcheck"
	"kube-enn-proxy/pkg/proxy"

	"github.com/vishvananda/netlink"
	//"kube-enn-proxy/pkg/util/async"
)

//var ipvsInterface utilipvs.Interface

const (
	sysctlBase               = "/proc/sys"
	sysctlRouteLocalnet      = "net/ipv4/conf/all/route_localnet"
	sysctlBridgeCallIPTables = "net/bridge/bridge-nf-call-iptables"
	sysctlVSConnTrack        = "net/ipv4/vs/conntrack"
	sysctlForward            = "net/ipv4/ip_forward"
)

const(
	syncFromServiceUpdate    = 1
	syncFromEnpointUpdate    = 2
	syncFromProxySyncPeriod  = 3
)

type Proxier struct {

	mu		    sync.Mutex
	serviceChanges      util.ServiceChangeMap
	endpointsChanges    util.EndpointsChangeMap
	serviceMap          util.ProxyServiceMap
	endpointsMap        util.ProxyEndpointMap
	portsMap            map[util.LocalPort]util.Closeable
	kernelIpvsMap       map[activeServiceKey][]activeServiceValue
	kernelClusterMap    map[string]int

	throttle            flowcontrol.RateLimiter

	syncPeriod	    time.Duration
	minSyncPeriod       time.Duration

	endpointsSynced     bool
	servicesSynced      bool
	initialized         int32
//	syncRunner          *async.BoundedFrequencyRunner // governs calls to syncProxyRules

	masqueradeAll       bool
	exec                utilexec.Interface
	clusterCIDR         string
	hostname            string
	nodeIP              net.IP
	nodeIPs             []net.IP
	nodeIPInterface     util.NodeIPInterface

	scheduler           string

	client              *kubernetes.Clientset

	ipvsInterface	    utilipvs.Interface

	recorder            record.EventRecorder
	portMapper          util.PortOpener
	healthChecker       healthcheck.Server

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

type endpointKey activeServiceValue

func NewProxier(
        clientset *kubernetes.Clientset,
        config    *options.KubeEnnProxyConfig,
        ipvs      utilipvs.Interface,
        execer    utilexec.Interface,
        hostname  string,
        nodeIP    net.IP,
        recorder  record.EventRecorder,
)(*Proxier, error){

	syncPeriod    := config.IpvsSyncPeriod
	minSyncPeriod := config.MinSyncPeriod

	// check valid user input
	if minSyncPeriod > syncPeriod {
		return nil, fmt.Errorf("min-sync (%v) must be < sync(%v)", minSyncPeriod, syncPeriod)
	}

	err := setNetFlag()
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: setNetFlag fall: %v", err)
	}

	var masqueradeAll = false
	if config.MasqueradeAll {
		masqueradeAll = true
	}

	clusterCIDR, err := util.GetPodCidrFromNodeSpec(clientset,config.HostnameOverride)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetPodCidr fall: %s", err.Error())
	}
	if len(clusterCIDR) == 0 {
		glog.Warningf("clusterCIDR not specified, unable to distinguish between internal and external traffic")
	}

//	node, err := util.GetNode(clientset,config.HostnameOverride)
//	if err != nil{
//		return nil, fmt.Errorf("NewProxier failure: GetNode fall: %s", err.Error())
//	}
//	hostname := node.Name

//	nodeIP, err := util.InternalGetNodeHostIP(node)
//	if err != nil{
//		return nil, fmt.Errorf("NewProxier failure: GetNodeIP fall: %s", err.Error())
//	}

	var scheduler = utilipvs.DEFAULSCHE
	if len(config.IpvsScheduler) != 0{
		scheduler = config.IpvsScheduler
	}


//	eventBroadcaster := record.NewBroadcaster()
//	scheme := runtime.NewScheme()
//	recorder := eventBroadcaster.NewRecorder(scheme,api.EventSource{Component: "kube-proxy", Host: hostname})

	healthchecker := healthcheck.NewServer(hostname,recorder,nil,nil)

	var throttle flowcontrol.RateLimiter
	if minSyncPeriod != 0{
		qps := float32(time.Second) / float32(minSyncPeriod)
		burst := 2
		glog.V(3).Infof("minSyncPeriod: %v, syncPeriod: %v, burstSyncs: %d", minSyncPeriod, syncPeriod, burst)
		throttle = flowcontrol.NewTokenBucketRateLimiter(qps,burst)
	}

	IpvsProxier := Proxier{
		serviceChanges:    util.NewServiceChangeMap(),
		endpointsChanges:  util.NewEndpointsChangeMap(hostname),
		serviceMap:        make(util.ProxyServiceMap),
		endpointsMap:      make(util.ProxyEndpointMap),
		portsMap:          make(map[util.LocalPort]util.Closeable),
		kernelIpvsMap:     make(map[activeServiceKey][]activeServiceValue),
		kernelClusterMap:  make(map[string]int),
		throttle:          throttle,
		masqueradeAll:     masqueradeAll,
		exec:              execer,
		clusterCIDR:       clusterCIDR,
		hostname:          hostname,
		nodeIP:            nodeIP,
		nodeIPInterface:   &util.NodeIP{},
		scheduler:         scheduler,
		syncPeriod:        syncPeriod,
		minSyncPeriod:     minSyncPeriod,
		client:            clientset,
		ipvsInterface:     ipvs,
		//iptablesInterface: iptInterface,
		recorder:          recorder,
		portMapper:        &util.ListenPortOpener{},
		healthChecker:     healthchecker,
	}

//	burstSyncs := 2
//	glog.V(3).Infof("minSyncPeriod: %v, syncPeriod: %v, burstSyncs: %d", minSyncPeriod, syncPeriod, burstSyncs)
//	IpvsProxier.syncRunner = async.NewBoundedFrequencyRunner("sync-runner", IpvsProxier.syncProxyRules, minSyncPeriod, syncPeriod, burstSyncs)

	return &IpvsProxier, nil

}

func FakeProxier() *Proxier{
	ipvs := utilipvs.NewEnnIpvs()
	return &Proxier{
		ipvsInterface: ipvs,
	}
}

// OnServiceAdd is called whenever creation of new service object is observed.
func (proxier *Proxier) OnServiceAdd(service *api.Service) {
	namespacedName := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	if proxier.serviceChanges.Update(&namespacedName, nil, service) && proxier.isInitialized() {
//		proxier.syncRunner.Run()
		proxier.syncProxyRules()
	}
}

// OnServiceUpdate is called whenever modification of an existing service object is observed.
func (proxier *Proxier) OnServiceUpdate(oldService, service *api.Service) {
	namespacedName := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	if proxier.serviceChanges.Update(&namespacedName, oldService, service) && proxier.isInitialized() {
//		proxier.syncRunner.Run()
		proxier.syncProxyRules()
	}
}

// OnServiceDelete is called whenever deletion of an existing service object is observed.
func (proxier *Proxier) OnServiceDelete(service *api.Service) {
	namespacedName := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	if proxier.serviceChanges.Update(&namespacedName, service, nil) && proxier.isInitialized() {
//		proxier.syncRunner.Run()
		proxier.syncProxyRules()
	}
}

// OnServiceSynced is called once all the initial even handlers were called and the state is fully propagated to local cache.
func (proxier *Proxier) OnServiceSynced() {
	proxier.mu.Lock()
	proxier.servicesSynced = true
	proxier.setInitialized(proxier.servicesSynced && proxier.endpointsSynced)
	proxier.mu.Unlock()

	// Sync unconditionally - this is called once per lifetime.
	proxier.syncProxyRules()
}


// OnEndpointsAdd is called whenever creation of new endpoints object is observed.
func (proxier *Proxier) OnEndpointsAdd(endpoints *api.Endpoints) {
	namespacedName := types.NamespacedName{Namespace: endpoints.Namespace, Name: endpoints.Name}
	if proxier.endpointsChanges.Update(&namespacedName, nil, endpoints) && proxier.isInitialized() {
//		proxier.syncRunner.Run()
		proxier.syncProxyRules()
	}
}

// OnEndpointsUpdate is called whenever modification of an existing endpoints object is observed.
func (proxier *Proxier) OnEndpointsUpdate(oldEndpoints, endpoints *api.Endpoints) {
	namespacedName := types.NamespacedName{Namespace: endpoints.Namespace, Name: endpoints.Name}
	if proxier.endpointsChanges.Update(&namespacedName, oldEndpoints, endpoints) && proxier.isInitialized() {
//		proxier.syncRunner.Run()
		proxier.syncProxyRules()
	}
}

// OnEndpointsDelete is called whenever deletion of an existing endpoints object is observed.
func (proxier *Proxier) OnEndpointsDelete(endpoints *api.Endpoints) {
	namespacedName := types.NamespacedName{Namespace: endpoints.Namespace, Name: endpoints.Name}
	if proxier.endpointsChanges.Update(&namespacedName, endpoints, nil) && proxier.isInitialized() {
//		proxier.syncRunner.Run()
		proxier.syncProxyRules()
	}
}

// OnEndpointsSynced is called once all the initial event handlers were called and the state is fully propagated to local cache.
func (proxier *Proxier) OnEndpointsSynced() {
	proxier.mu.Lock()
	proxier.endpointsSynced = true
	proxier.mu.Unlock()

	proxier.syncProxyRules()
}

func (proxier *Proxier) setInitialized(value bool) {
	var initialized int32
	if value {
		initialized = 1
	}
	atomic.StoreInt32(&proxier.initialized, initialized)
}

func (proxier *Proxier) isInitialized() bool {
	return atomic.LoadInt32(&proxier.initialized) > 0
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

	for{
		select {
		case <-t.C:
			glog.V(4).Infof("Periodic sync")
			proxier.Sync()
		case <-stopCh:
			glog.V(4).Infof("stop sync")
			return
		}
	}

//	proxier.syncRunner.Loop(wait.NeverStop)

}

func (proxier *Proxier) Sync(){
	err := proxier.EnsureIPtablesMASQ()
	if err!= nil{
		glog.Errorf("Sync: ennsure MASQ failed: %s", err)
	}
	proxier.syncProxyRules()
//	proxier.syncRunner.Run()
}


func (proxier *Proxier) syncProxyRules(){

	proxier.mu.Lock()
	defer proxier.mu.Unlock()

	if proxier.throttle != nil{
		proxier.throttle.Accept()
	}

	//glog.V(2).Infof("syncProxy called by %d time is %v", calledFunc, calledTime)

	start := time.Now()
	defer func() {
		glog.V(4).Infof("syncProxyRules took %v", time.Since(start))
	}()

	// don't sync rules till we've received services and endpoints
	if !proxier.endpointsSynced || !proxier.servicesSynced {
		glog.V(2).Info("Not syncing ipvs rules until Services and Endpoints have been received from master")
		return
	}

	activeServiceMap := make(map[activeServiceKey]bool)
	activeBindIP := make(map[string]int)

	serviceUpdateResult := util.UpdateServiceMap(
		proxier.serviceMap, &proxier.serviceChanges)
	endpointUpdateResult := util.UpdateEndpointsMap(
		proxier.endpointsMap, &proxier.endpointsChanges, proxier.hostname)

	staleServices := serviceUpdateResult.StaleServices
	// merge stale services gathered from updateEndpointsMap
	for svcPortName := range endpointUpdateResult.StaleServiceNames {
		if svcInfo, ok := proxier.serviceMap[svcPortName]; ok && svcInfo != nil && svcInfo.Protocol == strings.ToLower(string(api.ProtocolUDP)) {
			glog.V(2).Infof("Stale udp service %v -> %s", svcPortName, svcInfo.ClusterIP.String())
			staleServices.Insert(svcInfo.ClusterIP.String())
		}
	}

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
	proxier.nodeIPs, err = proxier.nodeIPInterface.GetNodeIPs(proxier.ipvsInterface)
	if err != nil {
		glog.Errorf("Failed to get node IP, err: %v", err)
	}

	glog.V(4).Infof("Syncing ipvs Proxier rules")


	if len(proxier.serviceChanges.Items) == 0 && len(proxier.endpointsChanges.Items) == 0{
		glog.V(4).Infof("nothing update on serviceChanges map or endpointChanges map, so sync the whole rules")

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
			if !ok {
				glog.V(3).Infof("new cluter need to bind so add ip to dummy link: %s", ipvs_cluster_service.ClusterIP.String())
				err = proxier.ipvsInterface.AddDummyClusterIp(ipvs_cluster_service.ClusterIP, dummylink)
				if err != nil {
					glog.Errorf("syncProxyRules: add dummy cluster ip feild: %s", err)
					continue
				}

			} else {
				glog.V(4).Infof("old cluter ip which already binded: %s", ipvs_cluster_service.ClusterIP.String())
			}
			activeBindIP[ipvs_cluster_service.ClusterIP.String()] = 1

			clusterKey := activeServiceKey{
				ip:       ipvs_cluster_service.ClusterIP.String(),
				port:     ipvs_cluster_service.Port,
				protocol: ipvs_cluster_service.Protocol,
			}

			activeServiceMap[clusterKey] = true

			KernelEndpoints, hasCluster := proxier.kernelIpvsMap[clusterKey]

			if err := proxier.CreateServiceRule(hasCluster, ipvs_cluster_service); err == nil {
				/* handle ipvs destination add */
				proxier.CreateEndpointRule(ipvs_cluster_service, svcName, KernelEndpoints)
			} else {
				glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s", err)
			}


			/* Capture externalIPs */
			glog.V(6).Infof("  -- capture external ip ")
			for _, externalIP := range serviceInfo.ExternalIPs {
				// If the "external" IP happens to be an IP that is local to this
				// machine, hold the local port open so no other process can open it
				// (because the socket might open but it would never work).
				if local, err := isLocalIP(externalIP); err != nil {
					glog.Errorf("can't determine if IP %s is local, assuming not: %v", externalIP, err)
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
								&api.ObjectReference{
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

				activeServiceMap[externalIPKey] = true

				KernelEndpoints, hasCluster := proxier.kernelIpvsMap[externalIPKey]

				if err := proxier.CreateServiceRule(hasCluster, ipvs_externalIp_service); err == nil {
					/* handle ipvs destination add */
					proxier.CreateEndpointRule(ipvs_externalIp_service, svcName, KernelEndpoints)
				} else {
					glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s", err)
				}

			}


			/* capture node port */
			glog.V(6).Infof("  -- capture node port ")
			if serviceInfo.NodePort != 0 {

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

				if proxier.nodeIPs == nil {
					glog.Errorf("Failed to get node IP")
				} else {
					for _, nodeIP := range proxier.nodeIPs {
						ipvs_node_service := &utilipvs.Service{
							ClusterIP:        nodeIP,
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
						activeServiceMap[nodeKey] = true

						KernelEndpoints, hasCluster := proxier.kernelIpvsMap[nodeKey]

						if err := proxier.CreateServiceRule(hasCluster, ipvs_node_service); err == nil {
							/* handle ipvs destination add */
							proxier.CreateEndpointRule(ipvs_node_service, svcName, KernelEndpoints)
						} else {
							glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s", err)
						}
					}
				}

			}

		}

		proxier.CheckUnusedRule(activeServiceMap, activeBindIP, dummylink)
	}

	if len(proxier.serviceChanges.Items) > 0{
		glog.V(4).Infof("serviceChange map updates, do sync service map")

		proxier.serviceChanges.Lock.Lock()

		for _, change := range proxier.serviceChanges.Items {
			// first delete svc then add svc
			for _, serviceInfo := range change.Previous {

				glog.V(6).Infof("sync serviceChange previous map")
				// delete previous services

				// delete service rule
				// note: we should not unbind dummy ip here because maybe other service share the same cluster ip
				// todo: need optimized because cannot unbind cluster ip on time
				proxier.deleteIpvsService(serviceInfo.ClusterIP,serviceInfo.Port,serviceInfo)

				// delete externalIP rule
				for _, externalIP := range serviceInfo.ExternalIPs {
					ip := net.ParseIP(externalIP)
					proxier.deleteIpvsService(ip,serviceInfo.Port,serviceInfo)
				}

				// delete nodeport rule
				if serviceInfo.NodePort != 0{
					if proxier.nodeIPs == nil {
						glog.Errorf("Failed to get node IP")
					}else {
						for _, nodeIP := range proxier.nodeIPs {
							proxier.deleteIpvsService(nodeIP,serviceInfo.NodePort,serviceInfo)
						}
					}
				}
			}

			for svcName, serviceInfo := range change.Current {

				glog.V(6).Infof("sync serviceChange current map")
				// add new services
				protocol := strings.ToLower(serviceInfo.Protocol)

				/* capture cluster ip */
				ipvs_cluster_service := &utilipvs.Service{
					ClusterIP:       serviceInfo.ClusterIP,
					Port:            serviceInfo.Port,
					Protocol:        serviceInfo.Protocol,
					Scheduler:       utilipvs.DEFAULSCHE,
					SessionAffinity: serviceInfo.SessionAffinity,

				}

				/* add cluster ip to dummy link */

				glog.V(3).Infof("new cluter need to bind so add ip to dummy link: %s", ipvs_cluster_service.ClusterIP.String())
				err = proxier.ipvsInterface.AddDummyClusterIp(ipvs_cluster_service.ClusterIP, dummylink)
				if err != nil {
					glog.Errorf("syncProxyRules: add dummy cluster ip feild: %s", err)
					continue
				}

				activeBindIP[ipvs_cluster_service.ClusterIP.String()] = 1

				err := proxier.CreateServiceRule(false, ipvs_cluster_service)
				if err != nil {
					glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s",err)
				}

				/* Capture externalIPs */
				glog.V(6).Infof("  -- capture external ip ")
				for _, externalIP := range serviceInfo.ExternalIPs {
					// If the "external" IP happens to be an IP that is local to this
					// machine, hold the local port open so no other process can open it
					// (because the socket might open but it would never work).
					if local, err := isLocalIP(externalIP); err != nil {
						glog.Errorf("can't determine if IP %s is local, assuming not: %v", externalIP, err)
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
									&api.ObjectReference{
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

					err := proxier.CreateServiceRule(false,ipvs_externalIp_service)
					if err != nil{
						glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s",err)
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

					if proxier.nodeIPs == nil {
						glog.Errorf("Failed to get node IP")
					}else{
						for _, nodeIP := range proxier.nodeIPs {
							ipvs_node_service := &utilipvs.Service{
								ClusterIP:        nodeIP,
								Port:             serviceInfo.NodePort,
								Protocol:         serviceInfo.Protocol,
								Scheduler:        utilipvs.DEFAULSCHE,
								SessionAffinity:  serviceInfo.SessionAffinity,
							}

							err := proxier.CreateServiceRule(false,ipvs_node_service)
							if err != nil{
								glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s",err)
							}
						}
					}


				}

			}
		}

		proxier.serviceChanges.Items = make(map[types.NamespacedName]*util.ServiceChange)
		proxier.serviceChanges.Lock.Unlock()
	}

	if len(proxier.endpointsChanges.Items) > 0{
		glog.V(4).Infof("endpointChange map update, do sync endpoint map")
		proxier.endpointsChanges.Lock.Lock()

		for _, change := range proxier.endpointsChanges.Items {
			for svcName, _ := range change.Previous{

				_, ok := change.Current[svcName]
				if ok{
					glog.V(6).Infof("skip sync endpoint previous map for %q, sync in current map", svcName)
					continue
				}

				serviceInfo, ok := proxier.serviceMap[svcName]
				if !ok{
					glog.Warningf("can not find service for %s, maybe service already deleted", svcName)
					continue
				}

				proxier.syncEndpointChange(serviceInfo.ClusterIP, serviceInfo.Port, serviceInfo, svcName)

				for _, externalIP := range serviceInfo.ExternalIPs {
					ip := net.ParseIP(externalIP)
					proxier.syncEndpointChange(ip, serviceInfo.Port, serviceInfo, svcName)
				}

				if serviceInfo.NodePort != 0{
					if proxier.nodeIPs == nil {
						glog.Errorf("Failed to get node IP")
					}else {
						for _, nodeIP := range proxier.nodeIPs {
							proxier.syncEndpointChange(nodeIP, serviceInfo.NodePort, serviceInfo, svcName)
						}
					}
				}

			}
			for svcName, _ := range change.Current{

				serviceInfo, ok := proxier.serviceMap[svcName]
				if !ok{
					glog.Errorf("can not find service for %s", svcName)
					continue
				}

				proxier.syncEndpointChange(serviceInfo.ClusterIP, serviceInfo.Port, serviceInfo, svcName)

				for _, externalIP := range serviceInfo.ExternalIPs {
					ip := net.ParseIP(externalIP)
					proxier.syncEndpointChange(ip, serviceInfo.Port, serviceInfo, svcName)
				}

				if serviceInfo.NodePort != 0{
					if proxier.nodeIPs == nil {
						glog.Errorf("Failed to get node IP")
					}else {
						for _, nodeIP := range proxier.nodeIPs {
							proxier.syncEndpointChange(nodeIP, serviceInfo.NodePort, serviceInfo, svcName)
						}
					}
				}
			}

		}


		proxier.endpointsChanges.Items = make(map[types.NamespacedName]*util.EndpointsChange)
		proxier.endpointsChanges.Lock.Unlock()
	}

	glog.V(4).Infof("create ipvs rule took %v", time.Since(start))

	// Close old local ports and save new ones.
	for k, v := range proxier.portsMap {
		if replacementPortsMap[k] == nil {
			v.Close()
		}
	}
	proxier.portsMap = replacementPortsMap

	// Finish housekeeping.
	// TODO: these could be made more consistent.
	for _, svcIP := range staleServices.UnsortedList() {
		if err := util.ClearUDPConntrackForIP(proxier.exec, svcIP); err != nil {
			glog.Errorf("Failed to delete stale service IP %s connections, error: %v", svcIP, err)
		}
	}
	proxier.deleteEndpointConnections(endpointUpdateResult.StaleEndpoints)

}

// After a UDP endpoint has been removed, we must flush any pending conntrack entries to it, or else we
// risk sending more traffic to it, all of which will be lost (because UDP).
// This assumes the proxier mutex is held
func (proxier *Proxier) deleteEndpointConnections(connectionMap map[util.EndpointServicePair]bool) {
	for epSvcPair := range connectionMap {
		if svcInfo, ok := proxier.serviceMap[epSvcPair.ServicePortName]; ok && svcInfo.Protocol == strings.ToLower(string(api.ProtocolUDP)) {
			endpointIP := util.IPPart(epSvcPair.Endpoint)
			err := util.ClearUDPConntrackForPeers(proxier.exec, svcInfo.ClusterIP.String(), endpointIP)
			if err != nil {
				glog.Errorf("Failed to delete %s endpoint connections, error: %v", epSvcPair.ServicePortName.String(), err)
			}
		}
	}
}

func (proxier *Proxier) deleteIpvsService(ClusterIP net.IP, Port int, serviceInfo *util.ServiceInfo){
	ipvs_cluster_service := &utilipvs.Service{
		ClusterIP:       ClusterIP,
		Port:            Port,
		Protocol:        serviceInfo.Protocol,
		Scheduler:       utilipvs.DEFAULSCHE,
		SessionAffinity: serviceInfo.SessionAffinity,

	}
	glog.V(3).Infof("delete previous ipvs service: %s:%d:%s",
		ipvs_cluster_service.ClusterIP.String(),
		ipvs_cluster_service.Port,
		ipvs_cluster_service.Protocol,
	)

	err := proxier.ipvsInterface.DeleteIpvsService(ipvs_cluster_service)

	if err != nil{
		glog.Errorf("clean unused ipvs service failed: %s",err)
	}
}

func (proxier *Proxier) syncEndpointChange(ClusterIP net.IP, Port int, serviceInfo *util.ServiceInfo, svcName proxy.ServicePortName){

	ipvs_cluster_service := &utilipvs.Service{
		ClusterIP:       ClusterIP,
		Port:            Port,
		Protocol:        serviceInfo.Protocol,
		Scheduler:       utilipvs.DEFAULSCHE,
		SessionAffinity: serviceInfo.SessionAffinity,

	}

	clusterKey := activeServiceKey{
		ip:       ipvs_cluster_service.ClusterIP.String(),
		port:     ipvs_cluster_service.Port,
		protocol: ipvs_cluster_service.Protocol,
	}

	KernelEndpoints, _ := proxier.kernelIpvsMap[clusterKey]

	proxier.CreateEndpointRule(ipvs_cluster_service,svcName,KernelEndpoints)
}

func (proxier *Proxier) CreateServiceRule(hasCluster bool, ipvs_cluster_service *utilipvs.Service) error{
	if !hasCluster {
		glog.V(3).Infof("new cluter find so add cluster to ipvs")

		/* handle ipvs service add */
		err := proxier.ipvsInterface.AddIpvsService(ipvs_cluster_service)
		if err != nil{
			return err
		}

	}else {
		glog.V(4).Infof("old cluter ip %s:%d",ipvs_cluster_service.ClusterIP.String(),ipvs_cluster_service.Port)
	}
	return nil
}

func (proxier *Proxier) CreateEndpointRule(ipvs_cluster_service *utilipvs.Service, svcName proxy.ServicePortName, KernelEndpoints []activeServiceValue){
	glog.V(6).Infof("sync endpoint rules of %q",svcName)

	oldDstMap := make(map[endpointKey]int)
	newDstMap := make(map[endpointKey]int)

	for _, oldDst := range KernelEndpoints{
		key := endpointKey{
			ip:    oldDst.ip,
			port:  oldDst.port,
		}
		oldDstMap[key] = 1
	}

	for _, newDst := range proxier.endpointsMap[svcName]{
		key := endpointKey{
			ip:    newDst.Ip,
			port:  newDst.Port,
		}
		newDstMap[key] = 1
	}

	for newEndpoint, _ := range newDstMap{
		_, ok := oldDstMap[newEndpoint]
		glog.V(6).Infof("try add endpoint %s:%d to service %s:%d",
			newEndpoint.ip,
			newEndpoint.port,
			ipvs_cluster_service.ClusterIP.String(),
			ipvs_cluster_service.Port,
		)

		if !ok{
			// add new dst
			ipvs_server := &utilipvs.Server{
				Ip:      newEndpoint.ip,
				Port:    newEndpoint.port,
				Weight:  utilipvs.DEFAULWEIGHT,
			}
			err := proxier.ipvsInterface.AddIpvsServer(ipvs_cluster_service, ipvs_server)
			if err != nil{
				glog.Errorf("syncProxyRules: add ipvs destination feild: %s",err)
				continue
			}
		}else{
			glog.V(6).Infof("endpoint is in ipvs rule so skip add endpoint")
		}
	}

	for oldEndpoint, _ := range oldDstMap{
		_, ok := newDstMap[oldEndpoint]
		if !ok{
			// delete old dst
			ipvs_server := &utilipvs.Server{
				Ip:      oldEndpoint.ip,
				Port:    oldEndpoint.port,
				Weight:  utilipvs.DEFAULWEIGHT,
			}
			err := proxier.ipvsInterface.DeleteIpvsServer(ipvs_cluster_service, ipvs_server)
			if err != nil{
				glog.Errorf("clean unused ipvs destination config failed: %s",err)
				continue
			}
		}
	}

}


/*  checkUnusedRule:
    compare active ipvs info with old ipvs info
    if there is any rule remain in the old ipvs info
    but doesn't exist in the active ipvs info
    then remove the rule
*/
func (proxier *Proxier) CheckUnusedRule (activeServiceMap map[activeServiceKey]bool, activeBindIP map[string]int, dummylink netlink.Link){
	// check unused ipvs rules
	oldSvcs, err := proxier.ipvsInterface.ListIpvsService()
	if err != nil{
		glog.Errorf("CheckUnusedRule: failed to list ipvs service: %v", err)
		return
	}
	for _, oldSvc := range oldSvcs{

		oldSvc_t, err := utilipvs.CreateInterService(oldSvc)
		serviceKey := activeServiceKey{
			ip:       oldSvc_t.ClusterIP.String(),
			port:     oldSvc_t.Port,
			protocol: oldSvc_t.Protocol,
		}
		glog.V(4).Infof("check active service: %s:%d:%s",serviceKey.ip,serviceKey.port,serviceKey.protocol)
		_, ok := activeServiceMap[serviceKey]

		/* unused service info so remove ipvs config and dummy cluster ip*/
		if !ok{
			glog.V(3).Infof("delete unused ipvs service config and dummy cluster ip: %s:%d:%s",serviceKey.ip,serviceKey.port,serviceKey.protocol)

			err = proxier.ipvsInterface.DeleteIpvsService(oldSvc_t)

			if err != nil{
				glog.Errorf("clean unused ipvs service failed: %s",err)
				continue
			}

		}
	}

	// check unused binded cluster ip
	oldBindIPs, _ := proxier.ipvsInterface.ListDuumyClusterIp(dummylink)
	if err != nil{
		glog.Errorf("CheckUnusedRule: failed to list binded ip: %v", err)
		return
	}

	for _, oldBindIP := range oldBindIPs{
		_, bind := activeBindIP[oldBindIP.IP.String()]
		if !bind{
			glog.V(3).Infof("unbinded unused ipvs dummy cluster ip: %s",oldBindIP.IP.String())

			err = proxier.ipvsInterface.DeleteDummyClusterIp(oldBindIP.IP, dummylink)
			if err != nil{
				glog.Errorf("clean unused dummy cluster ip failed: %s",err)
				continue
			}
		}
	}
}


func (proxier *Proxier) BuildIpvsMap() {

	ipvsMap := make(map[activeServiceKey][]activeServiceValue)

	ipvsSvcs, err := proxier.ipvsInterface.ListIpvsService()
	if err != nil{
		glog.Errorf("BuildIpvsMap: failed to list ipvs service: %v", err)
		proxier.kernelIpvsMap = ipvsMap
		return
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
	addrs, err := proxier.ipvsInterface.ListDuumyClusterIp(dummylink)
	if err != nil{
		glog.Errorf("failed to list binded ip: %v", err)
		return
	}

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


func CanUseIpvs() (bool, error){

	out, err := utilexec.New().Command("cut", "-f1", "-d", " ", "/proc/modules").CombinedOutput()
	if err != nil {
		return false, err
	}

	mods := strings.Split(string(out), "\n")
	wantModules := sets.NewString()
	loadModules := sets.NewString()
	wantModules.Insert(utilipvs.IpvsModules...)
	loadModules.Insert(mods...)
	modules := wantModules.Difference(loadModules).List()
	if len(modules) != 0 {
		return false, fmt.Errorf("Failed to load kernel modules: %v", modules)
	}
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

	if err := setSysctl(sysctlForward, 1); err != nil {
		return fmt.Errorf("can't set sysctl %s: %v", sysctlForward, err)
	}

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
