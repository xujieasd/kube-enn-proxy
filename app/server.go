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

	"kube-enn-proxy/app/options"
	"kube-enn-proxy/pkg/proxy/ipvs"
	"kube-enn-proxy/pkg/watchers"
)

type EnnProxyServer struct {

	Config	        *options.KubeEnnProxyConfig
	Client          *kubernetes.Clientset
	Proxier         *ipvs.Proxier

}

func NewEnnProxyServer(
	config	*options.KubeEnnProxyConfig,
	client  *kubernetes.Clientset,
	proxier	*ipvs.Proxier,

) (*EnnProxyServer, error){
	return  &EnnProxyServer{
		Config:		config,
		Client:         client,
		Proxier:	proxier,
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

	err = tryIPVSProxy()
	if(err != nil){
		return nil, err
	}

	proxier, err := ipvs.NewProxier(clientset, config)
	if(err != nil){
		return nil, err
	}

	err = createWatcher(clientset, config.ConfigSyncPeriod)
	if(err != nil){
		return nil, err
	}
	watchers.EndpointsWatchConfig.RegisterHandler(proxier)
	watchers.ServiceWatchConfig.RegisterHandler(proxier)

	return NewEnnProxyServer(config, clientset, proxier)

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
		glog.Errorf("can not ipvs proxy")
		return errors.New("can not ipvs proxy")
	}
	return nil
}

func CleanUpAndExit() {
	proxier := ipvs.FakeProxier()
	proxier.CleanupLeftovers()
}

func (s *EnnProxyServer) Run() error{

	glog.Infof("start run enn proxy")
	var StopCh chan struct{}
	var wg sync.WaitGroup

	StopCh = make(chan struct{})

	wg.Add(1)
	go s.Proxier.SyncLoop(StopCh, &wg)


	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch

	glog.Infof("get sys terminal and exit enn proxy")
	StopCh <- struct{}{}

	err := s.StopWatcher()
	if(err != nil){
		glog.Errorf("stop watcher failed %s",err.Error())
	}

	wg.Wait()

	return nil
}