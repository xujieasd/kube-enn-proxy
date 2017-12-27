package main

import (
	"fmt"
	"net"
	"syscall"
	"flag"
	"strings"
	"strconv"

	libipvs "github.com/docker/libnetwork/ipvs"
)

type Service struct{
	ClusterIP       net.IP
	Port            int
	Protocol        string
	Scheduler       string
	SessionAffinity bool
}
type Destination struct{
	Ip      string
	Port    int
	Weight  int
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

func ToProtocolNumber(pprotocal string) uint16{

	var protocol uint16
	if pprotocal == "tcp" {
		protocol = syscall.IPPROTO_TCP
	} else {
		protocol = syscall.IPPROTO_UDP
	}
	return protocol
}

func toolList(){
	handle, err := libipvs.New("")
	if err != nil {
		fmt.Printf("InitIpvsInterface failed Error: %v\n", err)
		panic(err)
	}

	svcs, err := handle.GetServices()
	if err != nil {
		fmt.Printf("GetServices failed Error: %v\n", err)
		panic(err)
	}
	fmt.Println("IP Virtual Server")
	fmt.Println("Prot LocalAddress:Port Scheduler Flags")
	fmt.Println("  -> RemoteAddress:Port           Forward Weight ActiveConn InActConn")
	for _, svc := range svcs {
		svc_ip := svc.Address.String()
		svc_port := int(svc.Port)
		svc_protocol := ToProtocolString(svc)
		svc_scheduler := svc.SchedName
		svc_flag := svc.Flags
		if svc_flag&0x0001 == 0{
			fmt.Printf("%s  %s:%d %s\n",svc_protocol,svc_ip,svc_port,svc_scheduler)
		}else{
			svc_timeout := int(svc.Timeout)
			fmt.Printf("%s  %s:%d %s persistent %d\n",svc_protocol,svc_ip,svc_port,svc_scheduler,svc_timeout)
		}

		dsts, err := handle.GetDestinations(svc)
		if err != nil {
			fmt.Printf("GetDestinations failed Error: %v\n", err)
			panic(err)
		}

		for _, dst := range dsts {
			dst_ip := dst.Address.String()
			dst_port := int(dst.Port)
			dst_weight := int(dst.Weight)
			dst_address := fmt.Sprintf("  -> %s:%d",dst_ip,dst_port)
			dst_masq := "Masq"
			fmt.Printf("%-34s%s    %d      0          0\n",dst_address,dst_masq,dst_weight)

		}

	}
}

func toolAddService(protocol string, service string, scheduler string){

	address := strings.Split(service,":")
	fmt.Printf("service: %s:%s:%s scheduler: %s",address[0],address[1],protocol,scheduler)


	handle, err := libipvs.New("")
	if err != nil {
		fmt.Printf("InitIpvsInterface failed Error: %v\n", err)
		panic(err)
	}

	svcIp := net.ParseIP(address[0])
	svcPort, err := strconv.Atoi(address[1])
	if err != nil{
		fmt.Printf("invalid port: %v", err)
		return
	}

	svc := &libipvs.Service{
		Address:       svcIp,
		AddressFamily: syscall.AF_INET,
		Protocol:      ToProtocolNumber(protocol),
		Port:          uint16(svcPort),
		SchedName:     scheduler,
	}
	if err := handle.NewService(svc); err != nil {
		fmt.Errorf("AddIpvsService: create service %s:%s:%s failed: %v",
			address[0],
			ToProtocolNumber(protocol),
			address[1],
			err,
		)
	}
}

func toolDelService(protocol string, service string){

	address := strings.Split(service,":")
	fmt.Printf("service: %s:%s:%s",address[0],address[1],protocol)

	handle, err := libipvs.New("")
	if err != nil {
		fmt.Printf("InitIpvsInterface failed Error: %v\n", err)
		panic(err)
	}

	svcIp := net.ParseIP(address[0])
	svcPort, err := strconv.Atoi(address[1])
	if err != nil{
		fmt.Printf("invalid port: %v", err)
		return
	}

	svc := &libipvs.Service{
		Address:       svcIp,
		AddressFamily: syscall.AF_INET,
		Protocol:      ToProtocolNumber(protocol),
		Port:          uint16(svcPort),
	}

	if err := handle.DelService(svc); err != nil {
		fmt.Errorf("DeleteIpvsService: delete service %s:%s:%s failed: %v",
			address[0],
			ToProtocolNumber(protocol),
			address[1],
			err,
		)
	}
}

func toolAddServer(protocol string, service string, server string){

	address := strings.Split(service,":")
	destination := strings.Split(server,":")
	fmt.Printf("service: %s:%s, server: %s:%s, protocol: %s",address[0],address[1],destination[0],destination[1],protocol)

	handle, err := libipvs.New("")
	if err != nil {
		fmt.Printf("InitIpvsInterface failed Error: %v\n", err)
		panic(err)
	}

	svcIp := net.ParseIP(address[0])
	svcPort, err := strconv.Atoi(address[1])
	if err != nil{
		fmt.Printf("invalid svc port: %v", err)
		return
	}

	svc := &libipvs.Service{
		Address:       svcIp,
		AddressFamily: syscall.AF_INET,
		Protocol:      ToProtocolNumber(protocol),
		Port:          uint16(svcPort),
	}

	dstIp := net.ParseIP(destination[0])
	dstPort, err := strconv.Atoi(address[1])
	if err != nil{
		fmt.Printf("invalid dst port: %v", err)
		return
	}

	dst := &libipvs.Destination{
		Address:       dstIp,
		AddressFamily: syscall.AF_INET,
		Port:          uint16(dstPort),
		Weight:        1,
	}

	err = handle.NewDestination(svc, dst)
	if err != nil {
		fmt.Errorf("AddIpvsServer destination %s:%s to the service %s:%s:%s failed: %v",
			destination[0],
			destination[1],
			address[0],
			protocol,
			address[1],
			err,
		)
	}

}

func toolDelServer(protocol string, service string, server string){

	address := strings.Split(service,":")
	destination := strings.Split(server,":")
	fmt.Printf("service: %s:%s, server: %s:%s, protocol: %s",address[0],address[1],destination[0],destination[1],protocol)

	handle, err := libipvs.New("")
	if err != nil {
		fmt.Printf("InitIpvsInterface failed Error: %v\n", err)
		panic(err)
	}

	svcIp := net.ParseIP(address[0])
	svcPort, err := strconv.Atoi(address[1])
	if err != nil{
		fmt.Printf("invalid svc port: %v", err)
		return
	}

	svc := &libipvs.Service{
		Address:       svcIp,
		AddressFamily: syscall.AF_INET,
		Protocol:      ToProtocolNumber(protocol),
		Port:          uint16(svcPort),
	}

	dstIp := net.ParseIP(destination[0])
	dstPort, err := strconv.Atoi(address[1])
	if err != nil{
		fmt.Printf("invalid dst port: %v", err)
		return
	}

	dst := &libipvs.Destination{
		Address:       dstIp,
		AddressFamily: syscall.AF_INET,
		Port:          uint16(dstPort),
		Weight:        1,
	}

	err = handle.DelDestination(svc, dst)
	if err != nil {
		fmt.Errorf("DeleteIpvsServer: delete destination %s:%s to the service %s:%s:%s failed: %v",
			destination[0],
			destination[1],
			address[0],
			protocol,
			address[1],
			err,
		)
	}

}

func main(){

	list := flag.Bool("list", false, "list all ipvs rules")
	add_service := flag.Bool("A", false, "add virtual service with options")
	del_service := flag.Bool("D", false, "delele virtual service")
	add_server  := flag.Bool("a", false, "add real server with options")
	del_server  := flag.Bool("d", false, "delete real servier")
	tcp_service := flag.String("t", "nil", "tcp service-address is host[:port]")
	udp_service := flag.String("u", "nil", "udp service-address is host[:port]")
	scheduler   := flag.String("s", "wlc", "one of rr|wrr|lc|wlc|lblc|lblcr|dh|sh|sed|nq, the default is wlc")
	real_server := flag.String("r", "nil", "server address is host (adn port)")


	flag.Parse()

	if *list{
		toolList()

	}else if *add_service{
		if *tcp_service != "nil"{
			toolAddService("tcp", *tcp_service, *scheduler)
		}else if *udp_service != "nil"{
			toolAddService("udp", *udp_service, *scheduler)
		}

	}else if *del_service{
		if *tcp_service != "nil"{
			toolDelService("tcp", *tcp_service)
		}else if *udp_service != "nil"{
			toolDelService("udp", *udp_service)
		}

	}else if *add_server{
		if *tcp_service != "nil"{
			toolAddServer("tcp", *tcp_service, *real_server)
		}else if *udp_service != "nil"{
			toolAddServer("udp", *udp_service, *real_server)
		}

	}else if *del_server{
		if *tcp_service != "nil"{
			toolDelServer("tcp", *tcp_service, *real_server)
		}else if *udp_service != "nil"{
			toolDelServer("udp", *udp_service, *real_server)
		}

	}

}