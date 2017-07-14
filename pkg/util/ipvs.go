package util

import (
	//"errors"
	//"fmt"
	//"io/ioutil"
	"net"
	//"reflect"
	//"strconv"
	//"strings"
	//"sync"
	"syscall"
	//"time"


	"github.com/golang/glog"
	"github.com/vishvananda/netlink"
	"github.com/mqliang/libipvs"

)

const (
	ENN_DUMMY          = "enn-dummy"
	DEFAULSCHE         = libipvs.RoundRobin
	IFACE_NOT_FOUND    = "Link not found"
	IFACE_HAS_ADDR     = "file exists"
	IPVS_SERVER_EXISTS = "file exists"

)


//var (
//	Ipvs_handle	libipvs.IPVSHandle
//)

type Service struct{
	clusterIP       net.IP
	port            int
	protocol        string
	nodePort        int
	Scheduler       string
	sessionAffinity bool
}
type Server struct{
	ip      string
	port    int
	Weight  int
}


type EnnIpvs struct {
	Ipvs_handle	libipvs.IPVSHandle
	//Service		ipvs.Service
	//Destination	ipvs.Destination
}

type Interface interface {
	//InitIpvsInterface() error
	AddIpvsService(service *Service) error
	DeleteIpvsService(service *Service) error
	AddIpvsServer(service *Service, dest *Server) error
	DeleteIpvsServer(service *Service, dest *Server) error
	GetDummyLink() (netlink.Link, error)
	AddDummyLink() error
	SetDummyLinkUp(link netlink.Link) error
	DeleteDummyLink() error
	AddDummyClusterIp(service *Service, link netlink.Link) error
	DeleteDummyClusterIp(service *Service, link netlink.Link) error
}

func NewEnnIpvs() Interface{

	handle, err := libipvs.New()
	if err != nil {
		glog.Errorf("InitIpvsInterface failed Error: %v", err)
		panic(err)
	}

	var ei = &EnnIpvs{
		Ipvs_handle: handle,
	}

	return ei
}

/*
func (ei *EnnIpvs) InitIpvsInterface() error {

	handle, err := libipvs.New()
	if err != nil {
		glog.Errorf("InitIpvsInterface failed Error: %v", err)
		panic(err)
	}

	Ipvs_handle = handle

	return nil
}
*/

func (ei *EnnIpvs) AddIpvsService(service *Service) error{

	return nil
}

func (ei *EnnIpvs) DeleteIpvsService(service *Service) error{

	return nil
}

func (ei *EnnIpvs) AddIpvsServer(service *Service, dest *Server) error{

	return nil
}

func (ei *EnnIpvs) DeleteIpvsServer(service *Service, dest *Server) error{

	return nil
}


func (ei *EnnIpvs) GetDummyLink() (netlink.Link, error){

	var link netlink.Link
	link, err := netlink.LinkByName(ENN_DUMMY)
	if err != nil  {
		if err.Error() == IFACE_NOT_FOUND {
			glog.Infof("GetDummyLink: get dummy link failed, then create dummy link")
			err = ei.AddDummyLink()
			if (err != nil) {
				panic("GetDummyLink: add dummy link failed:  " + err.Error())

			}
			link, err = netlink.LinkByName(ENN_DUMMY)
			/*
			err = ei.SetDummyLinkUp(link)

			if(err != nil) {
				return nil, err
			}
			*/
		} else{
			return nil, err
		}

	}

	return link, err
}

func (ei *EnnIpvs) AddDummyLink() error{
	err := netlink.LinkAdd(&netlink.Dummy{
		netlink.LinkAttrs{
			Name: ENN_DUMMY,
		},
	})
	if(err != nil){
		glog.Errorf("AddDummyLink: add dummy link failed %s", err)
		return err
	}

	return nil
}

func (ei *EnnIpvs) SetDummyLinkUp(link netlink.Link) error{
	err := netlink.LinkSetUp(link)
	if err != nil {
		glog.Errorf("SetDummyLinkUp: set dummy link up failed %s", err)
		//panic("Failed to bring dummy interface up: " + err.Error())
		return err
	}
	return nil
}

func (ei *EnnIpvs) DeleteDummyLink() error{

	var link netlink.Link
	link, err := netlink.LinkByName(ENN_DUMMY)

	if err != nil{
		glog.Errorf("DeleteDummyLink: delete dummy link failed %s", err)
		return err
	}

	err = netlink.LinkDel(link)

	if err != nil{
		glog.Errorf("DeleteDummyLink: delete dummy link failed %s", err)
		return err
	}

	return nil
}

func (ei *EnnIpvs) AddDummyClusterIp(service *Service, link netlink.Link) error{
	vip := &netlink.Addr{
		IPNet: &net.IPNet{
			service.clusterIP,
			net.IPv4Mask(255, 255, 255, 255),
		},
		Scope: syscall.RT_SCOPE_LINK,
	}
	err := netlink.AddrAdd(link, vip)
	if err != nil && err.Error() != IFACE_HAS_ADDR {
		glog.Errorf("AddDummyClusterIp: add cluster ip failed %s", err)
		return err
	}
	return nil
}

func (ei *EnnIpvs) DeleteDummyClusterIp(service *Service, link netlink.Link) error{
	vip := &netlink.Addr{
		IPNet: &net.IPNet{
			service.clusterIP,
			net.IPv4Mask(255, 255, 255, 255),
		},
		Scope: syscall.RT_SCOPE_LINK,
	}
	err := netlink.AddrDel(link, vip)
	if err != nil {
		glog.Errorf("DeleteDummyClusterIp: delete cluster ip failed %s", err)
		return err
	}
	return nil
}

func NewIpvsService() *Service{

	return nil
}

func NewIPVSServer() *Server{

	return nil
}



