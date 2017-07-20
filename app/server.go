package app

import (
	//"errors"
	//"os"
	//"os/signal"
	"sync"
	//"syscall"

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

	EndpointConfig  *watchers.EndpointsWatcher
	ServiceConfig   *watchers.ServicesWatcher

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

	clientconfig, err := clientcmd.BuildConfigFromFlags(config.Master, config.Kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	clientset, err := kubernetes.NewForConfig(clientconfig)
	if err != nil {
		panic(err.Error())
	}

	proxier, err := ipvs.NewProxier(clientset, config)

	if(err != nil){
		return nil, err
	}

	return NewEnnProxyServer(config, clientset, proxier)

}


func (s *EnnProxyServer) StartWatcher() error{
	ew, err := watchers.StartEndpointWatcher(s.Client, s.Config.ConfigSyncPeriod)

	if err != nil {
		panic(err.Error())
	}

	s.EndpointConfig = ew

	sw, err := watchers.StartServiceWatcher(s.Client, s.Config.ConfigSyncPeriod)

	if err != nil {
		panic(err.Error())
	}

	s.ServiceConfig = sw

	return nil
}

func (s *EnnProxyServer) StopWatcher() error{

	s.EndpointConfig.StopEndpointsWatcher()
	s.ServiceConfig.StopEndpointsWatcher()

	return nil
}

func (s *EnnProxyServer) Run() error{

	var StopCh chan struct{}
	var wg sync.WaitGroup

	err:= s.StartWatcher()
	if(err != nil){
		return err
	}

	StopCh = make(chan struct{})
	wg.Add(1)

	s.Proxier.SyncLoop(StopCh, &wg)

	return nil
}