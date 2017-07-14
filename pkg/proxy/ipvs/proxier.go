package ipvs

import (
	//"errors"
	//"fmt"
	//"io/ioutil"
	//"net"
	//"reflect"
	//"strconv"
	//"strings"
	"sync"
	//"syscall"
	//"time"


	//"github.com/golang/glog"
	//"github.com/vishvananda/netlink"
	//"github.com/mqliang/libipvs"

	ipvsutil "kube-enn-proxy/pkg/util"

)

//var ipvsInterface ipvsutil.Interface

type Proxier struct {

	mu		sync.Mutex
	ipvsInterface	ipvsutil.Interface


}

func NewProxier()(*Proxier, error){

	ipvs := ipvsutil.NewEnnIpvs()
	//err := ipvsInterface.InitIpvsInterface()

	IpvsProxier := Proxier{
		ipvsInterface: ipvs,
	}

	return &IpvsProxier, nil

}

func (proxier *Proxier) Run(){

}

func (proxier *Proxier) Sync(){

}

func (proxier *Proxier) syncProxyRules(){

}

func (proxier *Proxier) OnEndpointsUpdate(){

}

func (proxier *Proxier) OnServiceUpdate(){

}

func (proxier *Proxier) CleanUp(){

}