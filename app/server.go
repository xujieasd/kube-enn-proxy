package app

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	api "k8s.io/api/core/v1"

	"kube-enn-proxy/app/options"
	"kube-enn-proxy/pkg/proxy/ipvs"
	"kube-enn-proxy/pkg/watchers"
	"kube-enn-proxy/pkg/proxy/util"
	proxyconfig "kube-enn-proxy/pkg/proxy/config"

	utilipvs "kube-enn-proxy/pkg/util/ipvs"
	//utiliptables "kube-enn-proxy/pkg/util/iptables"
	utilexec "k8s.io/utils/exec"
	//utildbus "kube-enn-proxy/pkg/util/dbus"
	"k8s.io/client-go/informers"
	"k8s.io/apimachinery/pkg/util/wait"
	"fmt"
)

type EnnProxyServer struct {

	Config	               *options.KubeEnnProxyConfig
	Client                 *kubernetes.Clientset
	Proxier                *ipvs.Proxier
	Recorder               record.EventRecorder
	NodeRef                *api.ObjectReference

	ConfigSyncPeriod       time.Duration
	ServiceEventHandler    proxyconfig.ServiceHandler
	EndpointsEventHandler  proxyconfig.EndpointsHandler

}

func NewEnnProxyServer(
        config                 *options.KubeEnnProxyConfig,
        client                 *kubernetes.Clientset,
        proxier                *ipvs.Proxier,
        recorder               record.EventRecorder,
        nodeRef                *api.ObjectReference,
        serviceEventHandler    proxyconfig.ServiceHandler,
        endpointsEventHandler  proxyconfig.EndpointsHandler,

) (*EnnProxyServer, error){
	return  &EnnProxyServer{
		Config:		       config,
		Client:                client,
		Proxier:	       proxier,
		Recorder:              recorder,
		NodeRef:               nodeRef,
		ConfigSyncPeriod:      config.ConfigSyncPeriod,
		ServiceEventHandler:   serviceEventHandler,
		EndpointsEventHandler: endpointsEventHandler,
	},nil
}

func NewEnnProxyServerDefault(config *options.KubeEnnProxyConfig) (*EnnProxyServer ,error){

	if config.Kubeconfig == "" && config.Master == "" {
		glog.Warningf("Neither --kubeconfig nor --master was specified.  Using default API client.  This might not work.")
		/*todo need modify default config path*/
		config.Kubeconfig = "/var/lib/kube-enn-proxy/kubeconfig"
	}

	clientconfig, err := clientcmd.BuildConfigFromFlags(config.Master, config.Kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(clientconfig)
	if err != nil {
		glog.Fatalf("Invalid API configuration: %v", err)
		panic(err.Error())
	}

	//protocol := utiliptables.ProtocolIpv4
	//if net.ParseIP(config.BindAddress).To4() == nil {
	//	protocol = utiliptables.ProtocolIpv6
	//}

	//var iptInterface utiliptables.Interface
	//var dbus utildbus.Interface

	execerInterface := utilexec.New()
	ipvsInterface := utilipvs.NewEnnIpvs()
	glog.V(0).Infof("insmod ipvs module")

	//dbus = utildbus.New()
	//iptInterface = utiliptables.New(execer, dbus, protocol)

	node, err := util.GetNode(clientset,config.HostnameOverride)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetNode fall: %s", err.Error())
	}
	hostname := node.Name

	nodeIP, err := util.InternalGetNodeHostIP(node)
	if err != nil{
		return nil, fmt.Errorf("NewProxier failure: GetNodeIP fall: %s", err.Error())
	}

	eventBroadcaster := record.NewBroadcaster()
	scheme := runtime.NewScheme()
	recorder := eventBroadcaster.NewRecorder(scheme,api.EventSource{Component: "kube-proxy", Host: hostname})

	nodeRef := &api.ObjectReference{
		Kind:      "Node",
		Name:      hostname,
		UID:       types.UID(hostname),
		Namespace: "",
	}

	err = tryIPVSProxy()
	if(err != nil){
		return nil, err
	}

	proxier, err := ipvs.NewProxier(
		clientset,
		config,
		ipvsInterface,
		execerInterface,
		hostname,
		nodeIP,
		recorder,
	)
	if(err != nil){
		return nil, err
	}

//	err = createWatcher(clientset, config.ConfigSyncPeriod)
//	if(err != nil){
//		return nil, err
//	}
//	watchers.EndpointsWatchConfig.RegisterHandler(proxier)
//	watchers.ServiceWatchConfig.RegisterHandler(proxier)

	var serviceEventHandler proxyconfig.ServiceHandler
	var endpointsEventHandler proxyconfig.EndpointsHandler

	serviceEventHandler = proxier
	endpointsEventHandler = proxier

	return NewEnnProxyServer(
		config,
		clientset,
		proxier,
		recorder,
		nodeRef,
		serviceEventHandler,
		endpointsEventHandler,
	)

}


func createWatcher(clientset *kubernetes.Clientset, resyncPeriod time.Duration) error{

	var err error

	_, err = watchers.NewEndpointWatcher(clientset, resyncPeriod)

	if err != nil {
		panic(err.Error())
	}

	_, err = watchers.NewServiceWatcher(clientset, resyncPeriod)

	if err != nil {
		panic(err.Error())
	}

	return nil
}

func (s *EnnProxyServer) StopWatcher() error{

	watchers.EndpointsWatchConfig.StopEndpointsWatcher()
	watchers.ServiceWatchConfig.StopServiceWatcher()

	return nil
}

func tryIPVSProxy() error{
	use, err := ipvs.CanUseIpvs()
	if err != nil {
		glog.Errorf("can not determine whether to use ipvs proxy")
		return err
	}
	if !use{
		glog.Errorf("can not use ipvs proxy")
		return errors.New("can not use ipvs proxy")
	}
	glog.V(1).Infof("now use IPVS proxy")
	return nil
}

func CleanUpAndExit() {
	proxier := ipvs.FakeProxier()
	proxier.CleanupLeftovers()
}

func (s *EnnProxyServer) birthCry() {
	s.Recorder.Eventf(s.NodeRef, api.EventTypeNormal, "Starting", "Starting kube-proxy.")
}

func (s *EnnProxyServer) Run() error{

	glog.V(0).Infof("start run enn proxy")
	var StopCh chan struct{}
	var wg sync.WaitGroup

	informerFactory := informers.NewSharedInformerFactory(s.Client, s.ConfigSyncPeriod)

	// Create configs (i.e. Watches for Services and Endpoints)
	// Note: RegisterHandler() calls need to happen before creation of Sources because sources
	// only notify on changes, and the initial update (on process start) may be lost if no handlers
	// are registered yet.
	serviceConfig := proxyconfig.NewServiceConfig(informerFactory.Core().V1().Services(), s.ConfigSyncPeriod)
	serviceConfig.RegisterEventHandler(s.ServiceEventHandler)
	go serviceConfig.Run(wait.NeverStop)

	endpointsConfig := proxyconfig.NewEndpointsConfig(informerFactory.Core().V1().Endpoints(), s.ConfigSyncPeriod)
	endpointsConfig.RegisterEventHandler(s.EndpointsEventHandler)
	go endpointsConfig.Run(wait.NeverStop)

	// This has to start after the calls to NewServiceConfig and NewEndpointsConfig because those
	// functions must configure their shared informer event handlers first.
	go informerFactory.Start(wait.NeverStop)

	// Birth Cry after the birth is successful
	s.birthCry()

	StopCh = make(chan struct{})

	wg.Add(1)
	go s.Proxier.SyncLoop(StopCh, &wg)


	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	glog.V(0).Infof("get sys terminal and exit enn proxy")
	StopCh <- struct{}{}

	err := s.StopWatcher()
	if(err != nil){
		glog.Errorf("stop watcher failed %s",err.Error())
	}

	wg.Wait()

	return nil
}