package ipvs

import (
	"errors"
	"fmt"
	//"io/ioutil"
	"net"
	//"reflect"
	"strconv"
//	"strings"
	//"sync"
	"syscall"
	//"time"


	"github.com/golang/glog"
	"github.com/vishvananda/netlink"
	//"github.com/mqliang/libipvs"
	libipvs "github.com/docker/libnetwork/ipvs"

	"strings"
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
	ClusterIP       net.IP
	Port            int
	Protocol        string
	Scheduler       string
	SessionAffinity bool
}
type Server struct{
	Ip      string
	Port    int
	Weight  int
}


type EnnIpvs struct {
	//Ipvs_handle	libipvs.IPVSHandle
	Ipvs_handle	*libipvs.Handle
	//Service		ipvs.Service
	//Destination	ipvs.Destination
}

type Interface interface {
	//InitIpvsInterface() error
	//ListIpvsService() ([]*Service,error)
	ListIpvsService() ([]*libipvs.Service,error)
	//ListIpvsServer(service *Service) ([]*Server,error)
	ListIpvsServer(service *libipvs.Service) ([]*Server,error)
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
	ListDuumyClusterIp(link netlink.Link) ([]netlink.Addr, error)
}

func NewEnnIpvs() Interface{

	//handle, err := libipvs.New()
	handle, err := libipvs.New("")
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
/*
func (ei *EnnIpvs) ListIpvsService() ([]*Service,error){

	svcs, err := ei.Ipvs_handle.ListServices()
	if err != nil {
		return nil, err
	}

	inter_svcs := make([]*Service,0)
	for _, ipvs_svc := range svcs{
		inter_svc, err := CreateInterService(ipvs_svc)
		if err != nil{
			return nil, err
		}
		inter_svcs = append(inter_svcs,inter_svc)
	}

	return inter_svcs, nil
}
*/

func (ei *EnnIpvs) ListIpvsService() ([]*libipvs.Service,error) {
	//svcs, err := ei.Ipvs_handle.ListServices()
	svcs, err := ei.Ipvs_handle.GetServices()
	return svcs, err
}
/*
func (ei *EnnIpvs) ListIpvsServer(service *Service) ([]*Server,error){

	ipvs_service, err := CreateLibIpvsService(service)
	if err != nil{
		return nil, err
	}
	dsts, err := ei.Ipvs_handle.ListDestinations(ipvs_service)
	if err != nil {
		return nil, err
	}

	inter_dsts := make([]*Server,0)
	for _, ipvs_dst := range dsts{
		inter_dst, err := CreateInterServer(ipvs_dst)
		if err != nil{
			return nil, err
		}
		inter_dsts = append(inter_dsts,inter_dst)
	}

	return inter_dsts, nil
}
*/
func (ei *EnnIpvs) ListIpvsServer(service *libipvs.Service) ([]*Server,error) {
	//dsts, err := ei.Ipvs_handle.ListDestinations(service)
	dsts, err := ei.Ipvs_handle.GetDestinations(service)
	if err != nil {
		return nil, err
	}

	inter_dsts := make([]*Server,0)
	for _, ipvs_dst := range dsts{
		inter_dst, err := CreateInterServer(ipvs_dst)
		if err != nil{
			return nil, err
		}
		inter_dsts = append(inter_dsts,inter_dst)
	}

	return inter_dsts, nil
}

func (ei *EnnIpvs) AddIpvsService(service *Service) error{

	protocol := ToProtocolNumber(service)

	//svcs, err := ei.Ipvs_handle.ListServices()
	svcs, err := ei.Ipvs_handle.GetServices()
	if err != nil {
		panic(err)
	}

	for _, svc := range svcs {
		if strings.Compare(service.ClusterIP.String(), svc.Address.String()) == 0 &&
			/*libipvs.Protocol(protocol)*/ protocol == svc.Protocol && uint16(service.Port) == svc.Port {
			glog.V(4).Infof("AddIpvsService: ipvs service already exists")
			return nil
		}
	}

	glog.V(2).Infof("AddIpvsService: add service: %s:%s:%s",
		service.ClusterIP.String(),
		//libipvs.Protocol(protocol),
		service.Protocol,
		strconv.Itoa(int(service.Port)),
	)

	svc, err:= CreateLibIpvsService(service)

	if err != nil{
		return err
	}

	if err := ei.Ipvs_handle.NewService(svc); err != nil {
		return fmt.Errorf("AddIpvsService: create service failed")
	}
	glog.V(4).Infof("AddIpvsService: added service done")


	return nil
}

func (ei *EnnIpvs) DeleteIpvsService(service *Service) error{

	//protocol := ToProtocolNumber(service)

	glog.V(2).Infof("DeleteIpvsService: delete service: %s:%s:%s",
		service.ClusterIP.String(),
		//libipvs.Protocol(protocol),
		service.Protocol,
		strconv.Itoa(int(service.Port)),
	)

	svc, err:= CreateLibIpvsService(service)

	if err != nil{
		return err
	}

	if err := ei.Ipvs_handle.DelService(svc); err != nil {
		return fmt.Errorf("DeleteIpvsService: delete service failed")
	}
	glog.V(4).Infof("DeleteIpvsService: delete service done")

	return nil
}

func (ei *EnnIpvs) AddIpvsServer(service *Service, server *Server) error{

	svc, err:= CreateLibIpvsService(service)
	dest, err:= CreateLibIpvsServer(server)

	if err != nil{
		return err
	}

	glog.V(2).Infof("AddIpvsServer: add destination %s:%s to the service %s:%s:%s",
		dest.Address,
		strconv.Itoa(int(dest.Port)),
		svc.Address,
		//svc.Protocol,
		ToProtocolString(svc),
		strconv.Itoa(int(svc.Port)),
	)

	err = ei.Ipvs_handle.NewDestination(svc, dest)
	if err == nil {
		glog.V(2).Infof("AddIpvsDestination: success")
		return nil
	}

	if strings.Contains(err.Error(), IPVS_SERVER_EXISTS) {
		glog.V(4).Infof("AddIpvsServer: already added")
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

	glog.V(2).Infof("DeleteIpvsServer: delete destination %s:%s to the service %s:%s:%s",
		dest.Address,
		strconv.Itoa(int(dest.Port)),
		svc.Address,
		//svc.Protocol,
		ToProtocolString(svc),
		strconv.Itoa(int(svc.Port)),
	)

	err = ei.Ipvs_handle.DelDestination(svc, dest)
	if err != nil {
		return fmt.Errorf("DelDestination: failed")
	}

	glog.V(4).Infof("DelDestination: success")
	return nil
}

func (ei *EnnIpvs) FlushIpvs() error{

	svcs, err := ei.Ipvs_handle.GetServices()
	if err != nil{
		return err
	}

	for _, svc := range svcs{
		err := ei.Ipvs_handle.DelService(svc)
		if err != nil{
			glog.Errorf("flush ipvs err: %v",err)
		}
	}

/*
	err := ei.Ipvs_handle.Flush()

	if err != nil {
		glog.Errorf("FlushIpvs: cleanup ipvs rules failed: ", err.Error())
		return err
	}
*/
	return nil
}

func (ei *EnnIpvs) GetDummyLink() (netlink.Link, error){

	var link netlink.Link
	link, err := netlink.LinkByName(ENN_DUMMY)
	if err != nil  {
		if err.Error() == IFACE_NOT_FOUND {
			glog.V(4).Infof("GetDummyLink: get dummy link failed, then create dummy link")
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
			service.ClusterIP,
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
			service.ClusterIP,
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

func (ei *EnnIpvs) ListDuumyClusterIp(link netlink.Link) ([]netlink.Addr, error){

	addrs, err:= netlink.AddrList(link,syscall.AF_INET)
	if err != nil {
		glog.Errorf("ListDuumyClusterIp: list dummy link ip failed %s", err)
		return nil, err
	}
	return addrs, nil
}


func CreateLibIpvsService(service *Service) (*libipvs.Service, error){

	if service == nil{
		glog.Errorf("CreateLibIpvsService: inter service cannot be null")
		return nil, errors.New("ipvs service is empty")
	}
	svc := &libipvs.Service{
		Address:       service.ClusterIP,
		AddressFamily: syscall.AF_INET,
		//Protocol:      libipvs.Protocol(ToProtocolNumber(service)),
		Protocol:      ToProtocolNumber(service),
		Port:          uint16(service.Port),
		SchedName:     service.Scheduler,
	}

	if service.SessionAffinity {
		// set bit to enable service persistence
		//svc.Flags.Flags |= (1 << 24)
		//svc.Flags.Mask |= 0xFFFFFFFF
		//svc.Flags |= 0x01
		svc.Flags |= (1 << 24)
		svc.Netmask |= 0xFFFFFFFF
		// todo: need to refer to k8s 1.8
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
		Address:       net.ParseIP(dest.Ip),
		AddressFamily: syscall.AF_INET,
		Port:          uint16(dest.Port),
		Weight:        DEFAULWEIGHT,
	}

	return dst, nil
}

func CreateInterService(ipvsSvc *libipvs.Service) (*Service, error){
	if ipvsSvc == nil{
		glog.Errorf("CreateInterService: lib service cannot be null")
		return nil, errors.New("ipvs service is empty")
	}

	svc := &Service{
		ClusterIP:    ipvsSvc.Address,
		Port:         int(ipvsSvc.Port),
		//Protocol:     ipvsSvc.Protocol.String(),
		Protocol:     ToProtocolString(ipvsSvc),
		Scheduler:    ipvsSvc.SchedName,
	}

	return svc, nil
}

func CreateInterServer(ipvsDst *libipvs.Destination)(*Server, error){
	if ipvsDst == nil{
		glog.Errorf("CreateInterService: lib dst cannot be null")
		return nil, errors.New("ipvs dst is empty")
	}

	dst := &Server{
		Ip:      ipvsDst.Address.String(),
		Port:    int(ipvsDst.Port),
		Weight:  int(ipvsDst.Weight),
	}

	return dst, nil
}

func ToProtocolNumber(service *Service) uint16{

	var protocol uint16
	if service.Protocol == "tcp" {
		protocol = syscall.IPPROTO_TCP
	} else {
		protocol = syscall.IPPROTO_UDP
	}
	return protocol
}

func ToProtocolString(service *libipvs.Service) string {

	var protocol string
	if service.Protocol == syscall.IPPROTO_TCP{
		protocol = "tcp"
	} else if service.Protocol == syscall.IPPROTO_UDP{
		protocol = "udp"
	} else {
		protocol = "unknow"
	}
	return protocol
}


