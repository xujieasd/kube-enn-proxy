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

	"github.com/coreos/go-iptables/iptables"

	ipvsutil "kube-enn-proxy/pkg/proxy/ipvs/util"
	"kube-enn-proxy/pkg/util"
	"kube-enn-proxy/app/options"
	"kube-enn-proxy/pkg/watchers"

)

//var ipvsInterface ipvsutil.Interface

const (
	sysctlBase               = "/proc/sys"
	sysctlRouteLocalnet      = "net/ipv4/conf/all/route_localnet"
	sysctlBridgeCallIPTables = "net/bridge/bridge-nf-call-iptables"
	sysctlVSConnTrack        = "net/ipv4/vs/conntrack"
	sysctlForward            = "net/ipv4/ip_forward"
)

type Proxier struct {

	mu		sync.Mutex
	serviceMap      util.ProxyServiceMap
	endpointsMap    util.ProxyEndpointMap

	syncPeriod	time.Duration
	minSyncPeriod   time.Duration


	masqueradeAll   bool
	clusterCIDR     string
	hostname        string
	nodeIP          net.IP
	scheduler       string

	client          *kubernetes.Clientset

	ipvsInterface	ipvsutil.Interface


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

	ipvs := ipvsutil.NewEnnIpvs()
	//err := ipvsInterface.InitIpvsInterface()
	glog.Infof("insmod ipvs module")

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
	var scheduler = ipvsutil.DEFAULSCHE
	if len(config.IpvsScheduler) != 0{
		scheduler = config.IpvsScheduler
	}
	
	IpvsProxier := Proxier{
		serviceMap:    make(util.ProxyServiceMap),
		endpointsMap:  make(util.ProxyEndpointMap),
		masqueradeAll: masqueradeAll,
		clusterCIDR:   clusterCIDR,
		hostname:      hostname,
		nodeIP:        nodeIP,
		scheduler:     scheduler,
		syncPeriod:    syncPeriod,
		minSyncPeriod: minSyncPeriod,
		client:        clientset,
		ipvsInterface: ipvs,
	}

	return &IpvsProxier, nil

}

func FakeProxier() *Proxier{
	ipvs := ipvsutil.NewEnnIpvs()
	return &Proxier{
		ipvsInterface: ipvs,
	}
}

func (proxier *Proxier) SyncLoop(stopCh <-chan struct{}, wg *sync.WaitGroup){

	glog.Infof("Run IPVS Proxier")
	t := time.NewTicker(proxier.syncPeriod)
	defer t.Stop()
	defer wg.Done()

	err := proxier.createIPtablesMASQ()
	if err!= nil{
		glog.Errorf("SyncLoop: create MASQ failed: %s", err)
		return
	}

	for {
		select {
		case <-t.C:
			glog.Infof("Periodic sync")
			proxier.Sync()
		case <-stopCh:
			glog.Infof("Stop sync")
			return
		}
	}
}

func (proxier *Proxier) Sync(){

	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	proxier.serviceMap = util.BuildServiceMap()
	proxier.endpointsMap = util.BuildEndPointsMap(proxier.hostname)
	proxier.syncProxyRules()

}

func (proxier *Proxier) syncProxyRules(){

	start := time.Now()
	defer func() {
		glog.Infof("syncProxyRules took %v", time.Since(start))
	}()

	activeServiceMap := make(map[activeServiceKey][]activeServiceValue)

	/* create duumy link */
	dummylink, err := proxier.ipvsInterface.GetDummyLink()
	if err != nil{
		glog.Errorf("syncProxyRules: get dummy link feild: %s",err)
		return
	}

	for svcName, serviceInfo := range proxier.serviceMap {

		var ipvs_node_service *ipvsutil.Service
		ipvs_cluster_service := &ipvsutil.Service{
			ClusterIP:       serviceInfo.ClusterIP,
			Port:            serviceInfo.Port,
			Protocol:        serviceInfo.Protocol,
			Scheduler:       ipvsutil.DEFAULSCHE,
			SessionAffinity: serviceInfo.SessionAffinity,

		}

		if serviceInfo.NodePort != 0{
			ipvs_node_service = &ipvsutil.Service{
				ClusterIP:        proxier.nodeIP,
				Port:             serviceInfo.NodePort,
				Protocol:         serviceInfo.Protocol,
				Scheduler:        ipvsutil.DEFAULSCHE,
				SessionAffinity:  serviceInfo.SessionAffinity,
			}
		}

		/* add cluster ip to dummy link */
		err = proxier.ipvsInterface.AddDummyClusterIp(ipvs_cluster_service,dummylink)
		if err != nil{
			glog.Errorf("syncProxyRules: add dummy cluster ip feild: %s",err)
			continue
		}

		/* handle ipvs service add */
		err = proxier.ipvsInterface.AddIpvsService(ipvs_cluster_service)
		if err != nil{
			glog.Errorf("syncProxyRules: add ipvs cluster service feild: %s",err)
			continue
		}

		var nodeKey activeServiceKey
		clusterKey := activeServiceKey{
			ip:       ipvs_cluster_service.ClusterIP.String(),
			port:     ipvs_cluster_service.Port,
			protocol: ipvs_cluster_service.Protocol,
		}
		activeServiceMap[clusterKey] = make([]activeServiceValue,0)

		if serviceInfo.NodePort != 0{
			err = proxier.ipvsInterface.AddIpvsService(ipvs_node_service)
			if err != nil{
				glog.Errorf("syncProxyRules: add ipvs node service feild: %s",err)
				continue
			}
			nodeKey = activeServiceKey{
				ip:       ipvs_node_service.ClusterIP.String(),
				port:     ipvs_node_service.Port,
				protocol: ipvs_node_service.Protocol,
			}
			activeServiceMap[nodeKey] = make([]activeServiceValue,0)
		}

		/* handle ipvs destination add */
		for _, endpointinfo := range proxier.endpointsMap[svcName]{
			ipvs_server := &ipvsutil.Server{
				Ip:      endpointinfo.Ip,
				Port:    endpointinfo.Port,
				Weight:  ipvsutil.DEFAULWEIGHT,
			}

			endpointValue := activeServiceValue{
				ip:    ipvs_server.Ip,
				port:  ipvs_server.Port,
			}

			err = proxier.ipvsInterface.AddIpvsServer(ipvs_cluster_service, ipvs_server)
			if err != nil{
				glog.Errorf("syncProxyRules: add ipvs destination feild: %s",err)
				continue
			}

			activeServiceMap[clusterKey] = append(activeServiceMap[clusterKey],endpointValue)

			if serviceInfo.NodePort != 0{
				err = proxier.ipvsInterface.AddIpvsServer(ipvs_node_service, ipvs_server)
				if err != nil{
					glog.Errorf("syncProxyRules: add ipvs destination feild: %s",err)
					continue

				}

				activeServiceMap[nodeKey] = append(activeServiceMap[nodeKey],endpointValue)
			}
		}

	}

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
		oldSvc_t, err := ipvsutil.CreateInterService(oldSvc)
		serviceKey := activeServiceKey{
			ip:       oldSvc_t.ClusterIP.String(),
			port:     oldSvc_t.Port,
			protocol: oldSvc_t.Protocol,
		}
		glog.Infof("check active service: %s:%d:%s",serviceKey.ip,serviceKey.port,serviceKey.protocol)
		activeEndpoints, ok := activeServiceMap[serviceKey]

		/* unused service info so remove ipvs config and dummy cluster ip*/
		if !ok{
			glog.Infof("delet unused ipvs service config and dummy cluster ip")
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
			oldDsts, err := proxier.ipvsInterface.ListIpvsServer(oldSvc)
			if err != nil{
				panic(err)
			}
			for _, oldDst := range oldDsts{
				glog.Infof("old dst %s:%d", oldDst.Ip,oldDst.Port)
				isActive := false
				for _, activeEP := range activeEndpoints{
					glog.Infof("active endpoints %s:%d", activeEP.ip, activeEP.port)
					if strings.Compare(activeEP.ip,oldDst.Ip) == 0 && activeEP.port == oldDst.Port{
						isActive = true
						glog.Infof("endpoints %s:%d still active",activeEP.ip,activeEP.port)
						break
					}
				}
				if !isActive{
					glog.Infof("delete unused ipvs destination")
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

}

func (proxier *Proxier) OnEndpointsUpdate(endpointsUpdate *watchers.EndpointsUpdate){
	proxier.mu.Lock()
	defer proxier.mu.Unlock()

	if !(watchers.ServiceWatchConfig.HasSynced() && watchers.EndpointsWatchConfig.HasSynced()) {
		glog.Infof("Skipping ipvs server sync because local cache is not synced yet")
	}

	newEndpointsMap := util.BuildEndPointsMap(proxier.hostname)

	if len(newEndpointsMap) != len(proxier.endpointsMap) || !reflect.DeepEqual(newEndpointsMap, proxier.endpointsMap) {
		proxier.endpointsMap = newEndpointsMap
		proxier.syncProxyRules()
	} else {
		glog.Infof("Skipping proxy ipvs rule sync on endpoint update because nothing changed")
	}

}

func (proxier *Proxier) OnServicesUpdate(servicesUpdate *watchers.ServicesUpdate){
	proxier.mu.Lock()
	defer proxier.mu.Unlock()

	if !(watchers.ServiceWatchConfig.HasSynced() && watchers.EndpointsWatchConfig.HasSynced()) {
		glog.Infof("Skipping ipvs server sync because local cache is not synced yet")
	}

	newServiceMap := util.BuildServiceMap()

	if len(newServiceMap) != len(proxier.serviceMap) || !reflect.DeepEqual(newServiceMap, proxier.serviceMap) {
		proxier.serviceMap = newServiceMap
		proxier.syncProxyRules()
	} else {
		glog.Infof("Skipping proxy ipvs rule sync on service update because nothing changed")
	}

}


func CanUseIpvs() (bool, error){

	/*todo: check whether IPVS can be used in nodes*/


	return true, nil
}

func (proxier *Proxier) createIPtablesMASQ() error{

	glog.Infof("createIPtablesMASQ: start")
	iptablehandler, err := iptables.New()
	if err != nil {
		return errors.New("createIPtablesMASQ: iptable init failed" + err.Error())
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
		err = iptablehandler.AppendUnique("nat", "POSTROUTING", args...)
		if err != nil {
			return errors.New("createIPtablesMASQ: iptables masq all failed" + err.Error())
		}
	}
	if len(proxier.clusterCIDR) > 0 {
		args = []string{
			"!", "-s", proxier.clusterCIDR,
			"-m", "ipvs", "--ipvs", "--vdir", "ORIGINAL", "--vmethod", "MASQ",
			"-m", "comment", "--comment", "IPVS SNAT",
			"-j", "MASQUERADE",
		}
		err = iptablehandler.AppendUnique("nat", "POSTROUTING", args...)
		if err != nil {
			return errors.New("createIPtablesMASQ: iptables masq failed" + err.Error())
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
	glog.Infof("cleanUpIPtablesMASQ")
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
	glog.Infof("cleanUpIpvs")
	proxier.ipvsInterface.FlushIpvs()
}

func (proxier *Proxier) cleanUpDummyLink(){
	glog.Infof("cleanUpDummyLink")
	/* dummy cluster ip will clean up together with dummy link*/
	proxier.ipvsInterface.DeleteDummyLink()
}

func setNetFlag() error{
	if err := setSysctl(sysctlRouteLocalnet, 1); err != nil {
		return fmt.Errorf("can't set sysctl %s: %v", sysctlRouteLocalnet, err)
	}

	if val, err := getSysctl(sysctlBridgeCallIPTables); err == nil && val != 1 {
		glog.Infof("missing br-netfilter module or unset sysctl br-nf-call-iptables; proxy may not work as intended")
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
