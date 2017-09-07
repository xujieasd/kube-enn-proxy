package main

import (
	"fmt"
	"net"
	"syscall"

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


func main(){

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

		fmt.Printf("%s  %s:%d %s\n",svc_protocol,svc_ip,svc_port,svc_scheduler)

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