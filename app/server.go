package app

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"


)

type EnnProxyServer struct {

	Config *options.KubeEnnProxyConfig

}

func NewEnnProxyServer(

) (*EnnProxyServer, error){
	return  &EnnProxyServer{

	},nil
}

func NewEnnProxyServerDefaul(config *options.KubeEnnProxyConfig) (*EnnProxyServer ,error){

}

func (s *EnnProxyServer) run() error{

}