package testing

import (
	"fmt"
	"net"
	"strings"
	"syscall"

	utilipvs "kube-enn-proxy/pkg/util/ipvs"
	libipvs "github.com/docker/libnetwork/ipvs"
	"github.com/vishvananda/netlink"
	"k8s.io/apimachinery/pkg/util/sets"
)

type ServiceKey struct {
	ip       string
	port     int
	protocol string
}

type ServiceValue struct {
	ip      string
	port    int
}

type Faker struct {
	IpvsMap     map[ServiceKey][]ServiceValue
	DummyMap    map[string]int
}

func NewFake() *Faker{
	return &Faker{
		IpvsMap:    make(map[ServiceKey][]ServiceValue),
		DummyMap:   make(map[string]int),
	}
}

func (f *Faker) GetIpvsService(service *utilipvs.Service) (*libipvs.Service,error) {

	key := ServiceKey{
		ip:       service.ClusterIP.String(),
		port:     service.Port,
		protocol: service.Protocol,
	}
	_, ok := f.IpvsMap[key]
	if !ok{
		return nil, fmt.Errorf("GetIpvsServer fail: service %s:%d not find in ipvs rule", service.ClusterIP.String(), service.Port)
	}

	svc := &libipvs.Service{
		Address:   service.ClusterIP,
		Protocol:  ToProtocolNumber(service.Protocol),
		Port:      uint16(service.Port),
	}
	return svc, nil
}

func (f *Faker) ListIpvsService() ([]*libipvs.Service,error){

	services := make([]*libipvs.Service,0)

	for k, _ := range f.IpvsMap{
		svc := &libipvs.Service{
			Address:	net.ParseIP(k.ip),
			Protocol:	ToProtocolNumber(k.protocol),
			Port:		uint16(k.port),
		}
		services = append(services, svc)
	}
	return services, nil
}

func (f *Faker) ListIpvsServer(service *libipvs.Service) ([]*utilipvs.Server,error){

	servers := make([]*utilipvs.Server,0)

	InterSvc, err := utilipvs.CreateInterService(service)
	if err != nil{
		return nil, fmt.Errorf("ListIpvsServer fail: %v", err)
	}
	key := ServiceKey{
		ip:       InterSvc.ClusterIP.String(),
		port:     InterSvc.Port,
		protocol: InterSvc.Protocol,
	}

	dsts, ok := f.IpvsMap[key]
	if !ok{
		return nil, fmt.Errorf("ListIpvsServer fail: service %s:%d not find in ipvs rule", InterSvc.ClusterIP.String(), InterSvc.Port)
	}

	for _, dst := range dsts{
		server := &utilipvs.Server{
			Ip:      dst.ip,
			Port:    dst.port,
		}
		servers = append(servers,server)
	}
	return servers, nil
}

func (f *Faker) AddIpvsService(service *utilipvs.Service) error{

	if service == nil{
		return fmt.Errorf("AddIpvsService fail: server is nil")
	}
	key := ServiceKey{
		ip:       service.ClusterIP.String(),
		port:     service.Port,
		protocol: service.Protocol,
	}

	_, ok := f.IpvsMap[key]
	if ok{
		return nil
	}
	f.IpvsMap[key] = make([]ServiceValue,0)
	return nil
}

func (f *Faker) DeleteIpvsService(service *utilipvs.Service) error{

	if service == nil{
		return fmt.Errorf("DeleteIpvsService fail: server is nil")
	}
	key := ServiceKey{
		ip:       service.ClusterIP.String(),
		port:     service.Port,
		protocol: service.Protocol,
	}
	delete(f.IpvsMap,key)
	return nil
}

func (f *Faker) AddIpvsServer(service *utilipvs.Service, server *utilipvs.Server) error{

	if service == nil || server == nil {
		return fmt.Errorf("AddIpvsServer fail: server or dest is nil")
	}

	key := ServiceKey{
		ip:       service.ClusterIP.String(),
		port:     service.Port,
		protocol: service.Protocol,
	}

	value := ServiceValue{
		ip:    server.Ip,
		port:  server.Port,
	}

	f.IpvsMap[key] = append(f.IpvsMap[key],value)
	return nil
}

func (f *Faker) DeleteIpvsServer(service *utilipvs.Service, server *utilipvs.Server) error{

	if service == nil || server == nil {
		return fmt.Errorf("DeleteIpvsServer fail: server or dest is nil")
	}

	key := ServiceKey{
		ip:       service.ClusterIP.String(),
		port:     service.Port,
		protocol: service.Protocol,
	}

	value := ServiceValue{
		ip:    server.Ip,
		port:  server.Port,
	}

	dsts, ok := f.IpvsMap[key]
	if !ok{
		return fmt.Errorf("DeleteIpvsServer fail: service %s:%d not find in ipvs rule", service.ClusterIP.String(), service.Port)
	}

	active := false
	var target int
	for target = range dsts{
		if strings.Compare(value.ip, dsts[target].ip) == 0 && value.port ==  dsts[target].port {
			//endpoint is in ipvs rule
			active = true
			break
		}
	}
	if !active {
		return fmt.Errorf("DeleteIpvsServer fail: dst %s:%d not find in service %s:%d", server.Ip, server.Port, service.ClusterIP.String(), service.Port)
	}

	f.IpvsMap[key] = append(f.IpvsMap[key][:target],f.IpvsMap[key][target+1:]...)
	return nil
}

func (f *Faker) FlushIpvs() error{

	f.IpvsMap = nil
	return nil
}

func (f *Faker) GetDummyLink() (netlink.Link, error){

	return nil, nil
}

func (f *Faker) AddDummyLink() error{

	return nil
}

func (f *Faker) SetDummyLinkUp(link netlink.Link) error{

	return nil
}

func (f *Faker) DeleteDummyLink() error{

	f.DummyMap = nil
	return nil
}

func (f *Faker) AddDummyClusterIp(clusterIP net.IP, link netlink.Link) error{

	f.DummyMap[clusterIP.String()] = 1
	return nil
}

func (f *Faker) DeleteDummyClusterIp(clusterIP net.IP, link netlink.Link) error{

	delete(f.DummyMap,clusterIP.String())
	return nil
}

func (f *Faker) ListDuumyClusterIp(link netlink.Link) ([]netlink.Addr, error){

	addrs := make([]netlink.Addr, 0)
	for k,_ := range f.DummyMap{
		vip := netlink.Addr{
			IPNet: &net.IPNet{
				net.ParseIP(k),
				net.IPv4Mask(255, 255, 255, 255),
			},
			Scope: syscall.RT_SCOPE_LINK,
		}
		addrs = append(addrs,vip)
	}
	return addrs, nil
}

func (f *Faker) GetLocalAddresses(Dev string) (sets.String, error) {

	return nil, nil
}

func ToProtocolNumber(str string) uint16{

	var protocol uint16
	if str == "tcp" {
		protocol = syscall.IPPROTO_TCP
	} else {
		protocol = syscall.IPPROTO_UDP
	}
	return protocol
}