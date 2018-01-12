package ipvs

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
	libipvs "github.com/docker/libnetwork/ipvs"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	ENN_DUMMY          = "enn-dummy"
	DEFAULSCHE         = libipvs.RoundRobin
	DEFAULWEIGHT       = 1
	IFACE_NOT_FOUND    = "Link not found"
	IFACE_HAS_ADDR     = "file exists"
	IPVS_SERVER_EXISTS = "file exists"

)

const (
	// DefaultTimeoutSeconds is the default timeout seconds
	// of Client IP based session affinity - 3 hours.
	DefaultTimeoutSeconds       = 180 * 60
	FlagPersistent              = 0x1
	DefaultMask                 = 0xFFFFFFFF

)

var IpvsModules = []string{
	"ip_vs",
	"ip_vs_rr",
	"nf_conntrack_ipv4",
}

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
	Ipvs_handle	*libipvs.Handle
}

type Interface interface {
	GetIpvsService(service *Service) (*libipvs.Service,error)
	ListIpvsService() ([]*libipvs.Service,error)
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
	AddDummyClusterIp(clusterIP net.IP, link netlink.Link) error
	DeleteDummyClusterIp(clusterIP net.IP, link netlink.Link) error
	ListDuumyClusterIp(link netlink.Link) ([]netlink.Addr, error)
	GetLocalAddresses(Dev string) (sets.String, error)
}

func NewEnnIpvs() Interface{

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

func (ei *EnnIpvs) GetIpvsService(service *Service) (*libipvs.Service,error) {

	svc, err:= CreateLibIpvsService(service)
	if err != nil{
		return nil, err
	}

	kernelSvc, err := ei.Ipvs_handle.GetService(svc)
	if err != nil {
		return nil, err
	}

	return kernelSvc, nil
}

func (ei *EnnIpvs) ListIpvsService() ([]*libipvs.Service,error) {

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

	svcs, err := ei.Ipvs_handle.GetServices()
	if err != nil {
		panic(err)
	}

	for _, svc := range svcs {
		if strings.Compare(service.ClusterIP.String(), svc.Address.String()) == 0 &&
			protocol == svc.Protocol && uint16(service.Port) == svc.Port {
			glog.V(4).Infof("AddIpvsService: ipvs service already exists")
			return nil
		}
	}

	glog.V(3).Infof("AddIpvsService: add service: %s:%s:%s",
		service.ClusterIP.String(),
		service.Protocol,
		strconv.Itoa(int(service.Port)),
	)

	svc, err:= CreateLibIpvsService(service)

	if err != nil{
		return err
	}

	if err := ei.Ipvs_handle.NewService(svc); err != nil {
		return fmt.Errorf("AddIpvsService: create service %s:%s:%s failed: %v",
			service.ClusterIP.String(),
			service.Protocol,
			strconv.Itoa(int(service.Port)),
			err,
		)
	}
	glog.V(6).Infof("AddIpvsService: added service done")


	return nil
}

func (ei *EnnIpvs) DeleteIpvsService(service *Service) error{

	//protocol := ToProtocolNumber(service)

	glog.V(3).Infof("DeleteIpvsService: delete service: %s:%s:%s",
		service.ClusterIP.String(),
		service.Protocol,
		strconv.Itoa(int(service.Port)),
	)

	svc, err:= CreateLibIpvsService(service)

	if err != nil{
		return err
	}

	if err := ei.Ipvs_handle.DelService(svc); err != nil {
		return fmt.Errorf("DeleteIpvsService: delete service %s:%s:%s failed: %v",
			service.ClusterIP.String(),
			service.Protocol,
			strconv.Itoa(int(service.Port)),
			err,
		)
	}
	glog.V(6).Infof("DeleteIpvsService: delete service done")

	return nil
}

func (ei *EnnIpvs) AddIpvsServer(service *Service, server *Server) error{

	svc, err:= CreateLibIpvsService(service)
	dest, err:= CreateLibIpvsServer(server)

	if err != nil{
		return err
	}

	glog.V(3).Infof("AddIpvsServer: add destination %s:%s to the service %s:%s:%s",
		dest.Address,
		strconv.Itoa(int(dest.Port)),
		svc.Address,
		ToProtocolString(svc),
		strconv.Itoa(int(svc.Port)),
	)

	err = ei.Ipvs_handle.NewDestination(svc, dest)
	if err == nil {
		glog.V(6).Infof("AddIpvsDestination: success")
		return nil
	}

	if strings.Contains(err.Error(), IPVS_SERVER_EXISTS) {
		glog.V(4).Infof("AddIpvsServer: already added")
	} else {
		return fmt.Errorf("AddIpvsServer destination %s:%s to the service %s:%s:%s failed: %v",
			dest.Address,
			strconv.Itoa(int(dest.Port)),
			svc.Address,
			ToProtocolString(svc),
			strconv.Itoa(int(svc.Port)),
			err,
		)
	}

	return nil
}

func (ei *EnnIpvs) DeleteIpvsServer(service *Service, server *Server) error{

	svc, err:= CreateLibIpvsService(service)
	dest, err:= CreateLibIpvsServer(server)

	if err != nil{
		return err
	}

	glog.V(3).Infof("DeleteIpvsServer: delete destination %s:%s to the service %s:%s:%s",
		dest.Address,
		strconv.Itoa(int(dest.Port)),
		svc.Address,
		ToProtocolString(svc),
		strconv.Itoa(int(svc.Port)),
	)

	err = ei.Ipvs_handle.DelDestination(svc, dest)
	if err != nil {
		return fmt.Errorf("DeleteIpvsServer: delete destination %s:%s to the service %s:%s:%s failed: %v",
			dest.Address,
			strconv.Itoa(int(dest.Port)),
			svc.Address,
			ToProtocolString(svc),
			strconv.Itoa(int(svc.Port)),
			err,
		)
	}

	glog.V(6).Infof("DelDestination: success")
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

	return nil
}

func (ei *EnnIpvs) GetDummyLink() (netlink.Link, error){

	var link netlink.Link
	link, err := netlink.LinkByName(ENN_DUMMY)
	if err != nil  {
		if err.Error() == IFACE_NOT_FOUND {
			glog.V(3).Infof("GetDummyLink: get dummy link failed, then create dummy link")
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

func (ei *EnnIpvs) AddDummyClusterIp(clusterIP net.IP, link netlink.Link) error{
	vip := &netlink.Addr{
		IPNet: &net.IPNet{
			clusterIP,
			net.IPv4Mask(255, 255, 255, 255),
		},
		Scope: syscall.RT_SCOPE_LINK,
	}
	err := netlink.AddrAdd(link, vip)
	if err != nil && err.Error() != IFACE_HAS_ADDR {
		glog.Errorf("AddDummyClusterIp: add cluster ip %s failed %s", clusterIP.String(), err)
		return err
	}
	return nil
}

func (ei *EnnIpvs) DeleteDummyClusterIp(clusterIP net.IP, link netlink.Link) error{
	vip := &netlink.Addr{
		IPNet: &net.IPNet{
			clusterIP,
			net.IPv4Mask(255, 255, 255, 255),
		},
		Scope: syscall.RT_SCOPE_LINK,
	}
	err := netlink.AddrDel(link, vip)
	if err != nil {
		glog.Errorf("DeleteDummyClusterIp: delete cluster ip %s failed %s", clusterIP.String(), err)
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


func (ei *EnnIpvs) GetLocalAddresses(Dev string) (sets.String, error) {


	linkIndex := -1
	if len(Dev) != 0 {
		link, err := netlink.LinkByName(Dev)
		if err != nil {
			return nil, fmt.Errorf("error get filter device %s, err: %v", Dev, err)
		}
		linkIndex = link.Attrs().Index
	}

	routeFilter := &netlink.Route{
		Table:    unix.RT_TABLE_LOCAL,
		Type:     unix.RTN_LOCAL,
		Protocol: unix.RTPROT_KERNEL,
	}
	filterMask := netlink.RT_FILTER_TABLE | netlink.RT_FILTER_TYPE | netlink.RT_FILTER_PROTOCOL

	// find filter device
	if linkIndex != -1 {
		routeFilter.LinkIndex = linkIndex
		filterMask |= netlink.RT_FILTER_OIF
	}

	routes, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, routeFilter, filterMask)
	if err != nil {
		return nil, fmt.Errorf("error list route table, err: %v", err)
	}

	res := sets.NewString()
	for _, route := range routes {
		if route.Src != nil {
			res.Insert(route.Src.String())
		}
	}
	return res, nil
}

func CreateLibIpvsService(service *Service) (*libipvs.Service, error){

	if service == nil{
		glog.Errorf("CreateLibIpvsService: inter service cannot be null")
		return nil, errors.New("ipvs service is empty")
	}
	svc := &libipvs.Service{
		Address:       service.ClusterIP,
		AddressFamily: syscall.AF_INET,
		Protocol:      ToProtocolNumber(service),
		Port:          uint16(service.Port),
		SchedName:     service.Scheduler,
	}

	if service.SessionAffinity {
		svc.Flags |= FlagPersistent
		svc.Netmask |= DefaultMask
		svc.Timeout = DefaultTimeoutSeconds
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


