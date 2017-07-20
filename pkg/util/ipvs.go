package util

import (
	"errors"
	"fmt"
	//"io/ioutil"
	"net"
	//"reflect"
	"strconv"
	"strings"
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
	DEFAULWEIGHT       = 1
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
	AddIpvsServer(service *Service, server *Server) error
	DeleteIpvsServer(service *Service, server *Server) error
	FlushIpvs() error
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

	protocol := ToProtocolNumber(service)

	glog.Infof("AddIpvsService: add service: %s:%s:%s",
		service.clusterIP.String(),
		libipvs.Protocol(protocol),
		strconv.Itoa(int(service.port)),
	)

	svcs, err := ei.Ipvs_handle.ListServices()
	if err != nil {
		panic(err)
	}

	for _, svc := range svcs {
		if strings.Compare(service.clusterIP.String(), svc.Address.String()) == 0 &&
			libipvs.Protocol(protocol) == svc.Protocol && uint16(service.port) == svc.Port {
			glog.Infof("AddIpvsService: ipvs service already exists")
			return nil
		}
	}

	svc, err:= CreateLibIpvsService(service)

	if err != nil{
		return err
	}

	if err := ei.Ipvs_handle.NewService(svc); err != nil {
		return fmt.Errorf("AddIpvsService: create service failed")
	}
	glog.Infof("AddIpvsService: added service done")


	return nil
}

func (ei *EnnIpvs) DeleteIpvsService(service *Service) error{

	protocol := ToProtocolNumber(service)

	glog.Infof("DeleteIpvsService: delete service: %s:%s:%s",
		service.clusterIP.String(),
		libipvs.Protocol(protocol),
		strconv.Itoa(int(service.port)),
	)

	svc, err:= CreateLibIpvsService(service)

	if err != nil{
		return err
	}

	if err := ei.Ipvs_handle.DelService(svc); err != nil {
		return fmt.Errorf("DeleteIpvsService: delete service failed")
	}
	glog.Infof("DeleteIpvsService: delete service done")

	return nil
}

func (ei *EnnIpvs) AddIpvsServer(service *Service, server *Server) error{

	svc, err:= CreateLibIpvsService(service)
	dest, err:= CreateLibIpvsServer(server)

	if err != nil{
		return err
	}

	glog.Infof("AddIpvsServer: add destination %s:%s to the service %s:%s:%s",
		dest.Address,
		strconv.Itoa(int(dest.Port)),
		svc.Address,
		svc.Protocol,
		strconv.Itoa(int(svc.Port)),
	)

	err = ei.Ipvs_handle.NewDestination(svc, dest)
	if err == nil {
		glog.Infof("AddIpvsServer: success")
		return nil
	}

	if strings.Contains(err.Error(), IPVS_SERVER_EXISTS) {
		glog.Infof("AddIpvsServer: already added")
	} else {
		return fmt.Errorf("AddIpvsServer: failed")
	}

	return nil
}

func (ei *EnnIpvs) DeleteIpvsServer(service *Service, server *Server) error{

	svc, err:= CreateLibIpvsService(service)
	dest, err:= CreateLibIpvsServer(server)

	if err != nil{
		return err
	}

	glog.Infof("DeleteIpvsServer: delete destination %s:%s to the service %s:%s:%s",
		dest.Address,
		strconv.Itoa(int(dest.Port)),
		svc.Address,
		svc.Protocol,
		strconv.Itoa(int(svc.Port)),
	)

	err = ei.Ipvs_handle.DelDestination(svc, dest)
	if err != nil {
		return fmt.Errorf("DelDestination: failed")
	}

	glog.Infof("DelDestination: success")
	return nil
}

func (ei *EnnIpvs) FlushIpvs() error{

	err := ei.Ipvs_handle.Flush()

	if err != nil {
		glog.Errorf("FlushIpvs: cleanup ipvs rules failed: ", err.Error())
		return err
	}

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


func CreateLibIpvsService(service *Service) (*libipvs.Service, error){

	if service == nil{
		glog.Errorf("CreateLibIpvsService: inter service cannot be null")
		return nil, errors.New("ipvs service is empty")
	}
	svc := &libipvs.Service{
		Address:       service.clusterIP,
		AddressFamily: syscall.AF_INET,
		Protocol:      libipvs.Protocol(ToProtocolNumber(service)),
		Port:          uint16(service.port),
		SchedName:     service.Scheduler,
	}

	if service.sessionAffinity {
		// set bit to enable service persistence
		svc.Flags.Flags |= (1 << 24)
		svc.Flags.Mask |= 0xFFFFFFFF
		svc.Timeout = 180 * 60
	}

	return svc, nil

}

func CreateLibIpvsServer(dest *Server)(*libipvs.Destination, error){

	if dest == nil{
		glog.Errorf("CreateLibIpvsServer: inter server cannot be null")
		return nil, errors.New("ipvs destination is empty")
	}

	dst := &libipvs.Destination{
		Address:       net.ParseIP(dest.ip),
		AddressFamily: syscall.AF_INET,
		Port:          uint16(dest.port),
		Weight:        DEFAULWEIGHT,
	}

	return dst, nil
}

func ToProtocolNumber(service *Service) uint16{

	var protocol uint16
	if service.protocol == "tcp" {
		protocol = syscall.IPPROTO_TCP
	} else {
		protocol = syscall.IPPROTO_UDP
	}
	return protocol
}

func NewIpvsService() *Service{

	return nil
}

func NewIPVSServer() *Server{

	return nil
}



