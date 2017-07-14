package app

import (
	//"errors"
	//"os"
	//"os/signal"
	//"sync"
	//"syscall"

	"kube-enn-proxy/app/options"
	"kube-enn-proxy/pkg/proxy/ipvs"
)

type EnnProxyServer struct {

	Config	*options.KubeEnnProxyConfig
	Proxier *ipvs.Proxier

}

func NewEnnProxyServer(
	config	*options.KubeEnnProxyConfig,
	proxier	*ipvs.Proxier,

) (*EnnProxyServer, error){
	return  &EnnProxyServer{
		Config:		config,
		Proxier:	proxier,
	},nil
}

func NewEnnProxyServerDefault(config *options.KubeEnnProxyConfig) (*EnnProxyServer ,error){

	proxier, err := ipvs.NewProxier()

	if(err != nil){
		return nil, err
	}


	return NewEnnProxyServer(config, proxier)

}

func (s *EnnProxyServer) Run() error{

	s.Proxier.Run()

	return nil
}