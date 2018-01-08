package util

import (
	"os"
	"fmt"
	"net"
	"strings"

	"k8s.io/client-go/kubernetes"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilipvs "kube-enn-proxy/pkg/util/ipvs"
	"github.com/golang/glog"
)

func GetHostname(hostnameOverride string) string {
	var hostname string = hostnameOverride
	if hostname == "" {
		nodename, err := os.Hostname()
		if err != nil {
			glog.Fatalf("Couldn't determine hostname: %v", err)
		}
		hostname = nodename
	}
	return strings.ToLower(strings.TrimSpace(hostname))
	//return hostname
}

func GetNode(clientset *kubernetes.Clientset, hostnameOverride string) (*apiv1.Node, error) {

	hostname := GetHostname(hostnameOverride)
	if hostname !="" {
		node, err := clientset.Core().Nodes().Get(hostname, metav1.GetOptions{})
		if err == nil {
			return node, nil
		}
	}

	return nil, fmt.Errorf("Failed to identify the node by hostname or --hostname-override")
}

func GetPodCidrFromNodeSpec(clientset *kubernetes.Clientset, hostnameOverride string) (string, error) {
	node, err := GetNode(clientset, hostnameOverride)
	if err != nil {
		return "", fmt.Errorf("Failed to get pod CIDR allocated for the node due to: " + err.Error())
	}
	return node.Spec.PodCIDR, nil
}

func InternalGetNodeHostIP(node *apiv1.Node) (net.IP, error) {
	addresses := node.Status.Addresses
	addressMap := make(map[apiv1.NodeAddressType][]apiv1.NodeAddress)
	for i := range addresses {
		addressMap[addresses[i].Type] = append(addressMap[addresses[i].Type], addresses[i])
	}
	if addresses, ok := addressMap[apiv1.NodeInternalIP]; ok {
		return net.ParseIP(addresses[0].Address), nil
	}
	if addresses, ok := addressMap[apiv1.NodeExternalIP]; ok {
		return net.ParseIP(addresses[0].Address), nil
	}
	return nil, fmt.Errorf("host IP unknown; known addresses: %v", addresses)
}

type NodeIPInterface interface {
	GetNodeIPs(ipvs utilipvs.Interface) ([]net.IP, error)
}

type NodeIP struct {}

func (ni *NodeIP) GetNodeIPs(ipvs utilipvs.Interface) ([]net.IP, error){
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("get node ip err: %v", err)
	}
	nodeAddress, err := ipvs.GetLocalAddresses("")
	if err != nil{
		return nil, fmt.Errorf("get node ip err: %v", err)
	}
	for i := range interfaces {
		name := interfaces[i].Name
		// todo: need to handle how to make 127.0.0.1:nodeport accsessible
		if (strings.HasPrefix(name, "docker")||
			strings.HasPrefix(name, "flannel")||
			strings.HasPrefix(name, "enn-dummy")||
			strings.HasPrefix(name, "cni")||
			strings.HasPrefix(name, "lo")) {
			fakeAddress, err := ipvs.GetLocalAddresses(name)
			if err != nil{
				return nil, fmt.Errorf("get node ip err: %v", err)
			}
			nodeAddress = nodeAddress.Difference(fakeAddress)

		}

	}
	var ips []net.IP
	for _, ipStr := range nodeAddress.UnsortedList() {
		ips = append(ips, net.ParseIP(ipStr))
	}
	return ips, err
}