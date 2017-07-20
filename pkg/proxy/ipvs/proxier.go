package ipvs

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"path"
	//"reflect"
	"strconv"
	"strings"
	"sync"
	//"syscall"
	"time"


	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"
	"github.com/coreos/go-iptables/iptables"

	ipvsutil "kube-enn-proxy/pkg/util"
	"kube-enn-proxy/app/options"
	//watchers "kube-enn-proxy/pkg/watchers"
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

	err := setNetFlag()
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: setNetFlag fall")
	}

	var masqueradeAll = false
	if config.MasqueradeAll {
		masqueradeAll = true
	}

	/*todo need to handle when CIDR not net*/
	clusterCIDR, err := ipvsutil.GetPodCidrFromNodeSpec(clientset,config.HostnameOverride)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetPodCidr fall: %s", err.Error())
	}

	node, err := ipvsutil.GetNode(clientset,config.HostnameOverride)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetNode fall: %s", err.Error())
	}
	hostname := node.Name

	nodeIP, err := ipvsutil.InternalGetNodeHostIP(node)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetNodeIP fall: %s", err.Error())
	}
	var scheduler = ipvsutil.DEFAULSCHE
	if len(config.IpvsScheduler) != 0{
		scheduler = config.IpvsScheduler
	}

	ipvs := ipvsutil.NewEnnIpvs()
	//err := ipvsInterface.InitIpvsInterface()

	IpvsProxier := Proxier{
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

func (proxier *Proxier) SyncLoop(stopCh <-chan struct{}, wg *sync.WaitGroup){

	glog.Infof("Run IPVS Proxier")
	t := time.NewTicker(proxier.syncPeriod)
	defer t.Stop()
	defer wg.Done()

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
	proxier.syncProxyRules()

}

func (proxier *Proxier) syncProxyRules(){

}

func (proxier *Proxier) OnEndpointsUpdate(){

}

func (proxier *Proxier) OnServicesUpdate(){

}

func buildServiceMap(){

}

func buildEndPointMap(){

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

func (proxier *Proxier) CleanUp(){

}