package ipvs

import (
	"testing"
	"fmt"
	"strings"
	"net"
	"kube-enn-proxy/pkg/proxy/util"
	"kube-enn-proxy/pkg/proxy"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakeexec "k8s.io/utils/exec/testing"

	ipvstest "kube-enn-proxy/pkg/util/ipvs/testing"
	utilipvs "kube-enn-proxy/pkg/util/ipvs"
	"k8s.io/apimachinery/pkg/types"
	api "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const testHostname = "test-hostname"

func Test_first(t *testing.T){
	t.Log("pass")
}

func FakeGetNodeIPs() (ips []net.IP) {
	ips = []net.IP{net.ParseIP("100.101.102.103")}
	return
}

type fakeClosable struct {
	closed bool
}

func (c *fakeClosable) Close() error {
	c.closed = true
	return nil
}

// fakePortOpener implements portOpener.
type fakePortOpener struct {
	openPorts []*util.LocalPort
}

// OpenLocalPort fakes out the listen() and bind() used by syncProxyRules
// to lock a local port.
func (f *fakePortOpener) OpenLocalPort(lp *util.LocalPort) (util.Closeable, error) {
	f.openPorts = append(f.openPorts, lp)
	return nil, nil
}


func makeTestService(namespace, name string, svcFunc func(*api.Service)) *api.Service {
	svc := &api.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{},
		},
		Spec:   api.ServiceSpec{},
		Status: api.ServiceStatus{},
	}
	svcFunc(svc)
	return svc
}

func makeServiceMap(proxier *Proxier, allServices ...*api.Service) {
	for i := range allServices {
		svcName := types.NamespacedName{
			Namespace: allServices[i].Namespace,
			Name:      allServices[i].Name,
		}
		for j := range allServices[i].Spec.Ports {
			servicePort := &allServices[i].Spec.Ports[j]

			serviceName := proxy.ServicePortName{
				NamespacedName: svcName,
				Port:           servicePort.Name,
			}

			info := util.NewServiceInfo(serviceName, servicePort, allServices[i])

			proxier.serviceMap[serviceName] = info
		}
	}

	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	proxier.servicesSynced = true
}

func makeTestEndpoints(namespace, name string, eptFunc func(*api.Endpoints)) *api.Endpoints {
	ept := &api.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	eptFunc(ept)
	return ept
}

func makeEndpointsMap(proxier *Proxier, allEndpoints ...*api.Endpoints) {
	for i := range allEndpoints {
		svcName := types.NamespacedName{
			Namespace: allEndpoints[i].Namespace,
			Name:      allEndpoints[i].Name,
		}
		for _, endpoints_sub := range allEndpoints[i].Subsets{
			for _, ports := range endpoints_sub.Ports{
				serviceName := proxy.ServicePortName{
					NamespacedName: svcName,
					Port:           ports.Name,
				}

				for _, addr := range endpoints_sub.Addresses{

					info := util.NewEndpointsInfo(addr,ports,testHostname)

					proxier.endpointsMap[serviceName] = append(proxier.endpointsMap[serviceName], info)
				}
			}
		}
	}

	proxier.mu.Lock()
	defer proxier.mu.Unlock()
	proxier.endpointsSynced = true
}

func makeNSN(namespace, name string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: namespace,
		Name: name,
	}
}

func NewFakeProxier() *Proxier{

	ipvs := ipvstest.NewFake()
	nodeIPs := FakeGetNodeIPs()

	return &Proxier{
		serviceMap:        make(util.ProxyServiceMap),
		endpointsMap:      make(util.ProxyEndpointMap),
		portsMap:          make(map[util.LocalPort]util.Closeable),
		kernelIpvsMap:     make(map[activeServiceKey][]activeServiceValue),
		kernelClusterMap:  make(map[string]int),
		clusterCIDR:       "10.0.0.0/24",
		hostname:          testHostname,
		nodeIPs:           nodeIPs,
		exec:              &fakeexec.FakeExec{},
		scheduler:         utilipvs.DEFAULSCHE,
		ipvsInterface:     ipvs,
		portMapper:        &fakePortOpener{[]*util.LocalPort{}},
	}
}

func TestClusterIP(t *testing.T){

	fp := NewFakeProxier()

	svcPortName := proxy.ServicePortName{
		NamespacedName: makeNSN("ns1", "svc1"),
		Port:           "p80",
	}

	svcIP := "10.20.30.41"
	svcPort := 80

	makeServiceMap(fp,
		makeTestService(svcPortName.Namespace, svcPortName.Name, func(svc *api.Service) {
			svc.Spec.Type = "ClusterIP"
			svc.Spec.ClusterIP = svcIP
			svc.Spec.Ports = []api.ServicePort{{
				Name:     svcPortName.Port,
				Port:     int32(svcPort),
				Protocol: api.ProtocolTCP,
			}}
		}),
	)

	epIP1 := "10.180.0.1"
	epPort1 := 80
	epIP2 := "10.180.0.2"
	epPort2 := 81

	makeEndpointsMap(fp,
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: epIP1,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(epPort1),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: epIP2,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(epPort2),
				}},
			}}
		}),
	)

	fp.syncProxyRules()

	checkBind, err := fp.hasBindIP("10.20.30.41")
	if !checkBind{
		t.Errorf("failed find bind service %v", err)
	}
	svcNumber, err := fp.checkSvcNumber()
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != 1{
		t.Errorf("svcNumber: %d, expected: 1", svcNumber)
	}

	endpointNumber, err := fp.checkEndpointNumber("10.20.30.41",80)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 2{
		t.Errorf("svcNumber: %d, expected: 2", endpointNumber)
	}
	checkRule, err := fp.hasRuleDestination("10.20.30.41",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}
	checkRule, err = fp.hasRuleDestination("10.20.30.41",80,"10.180.0.2",81)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}

}

func TestNodePort(t *testing.T) {

	fp := NewFakeProxier()

	svcPortName := proxy.ServicePortName{
		NamespacedName: makeNSN("ns1", "svc1"),
		Port:           "p80",
	}

	svcIP := "10.20.30.41"
	svcPort := 80
	svcNodePort := 30001

	makeServiceMap(fp,
		makeTestService(svcPortName.Namespace, svcPortName.Name, func(svc *api.Service) {
			svc.Spec.Type = "NodePort"
			svc.Spec.ClusterIP = svcIP
			svc.Spec.Ports = []api.ServicePort{{
				Name:     svcPortName.Port,
				Port:     int32(svcPort),
				Protocol: api.ProtocolTCP,
				NodePort: int32(svcNodePort),
			}}
		}),
	)

	epIP1 := "10.180.0.1"
	epPort1 := 80

	makeEndpointsMap(fp,
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: epIP1,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(epPort1),
				}},
			}}
		}),
	)

	fp.syncProxyRules()

	checkBind, err := fp.hasBindIP("10.20.30.41")
	if !checkBind{
		t.Errorf("failed find bind service %v", err)
	}
	svcNumber, err := fp.checkSvcNumber()
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != 2{
		t.Errorf("svcNumber: %d, expected: 2", svcNumber)
	}

	endpointNumber, err := fp.checkEndpointNumber("10.20.30.41",80)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("svcNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err := fp.hasRuleDestination("10.20.30.41",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}

	endpointNumber, err = fp.checkEndpointNumber("100.101.102.103",30001)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("svcNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err = fp.hasRuleDestination("100.101.102.103",30001,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}
}

func TestExternalIP (t *testing.T){

	fp := NewFakeProxier()

	svcPortName := proxy.ServicePortName{
		NamespacedName: makeNSN("ns1", "svc1"),
		Port:           "p80",
	}

	svcIP := "10.20.30.41"
	svcPort := 80
	svcExternalIPs := []string{"50.60.70.80", "50.60.70.81"}

	makeServiceMap(fp,
		makeTestService(svcPortName.Namespace, svcPortName.Name, func(svc *api.Service) {
			svc.Spec.Type = "ClusterIP"
			svc.Spec.ClusterIP = svcIP
			svc.Spec.ExternalIPs = svcExternalIPs
			svc.Spec.Ports = []api.ServicePort{{
				Name:       svcPortName.Port,
				Port:       int32(svcPort),
				Protocol:   api.ProtocolTCP,
				TargetPort: intstr.FromInt(svcPort),
			}}
		}),
	)

	epIP1 := "10.180.0.1"
	epPort1 := 80

	makeEndpointsMap(fp,
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: epIP1,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(epPort1),
				}},
			}}
		}),
	)

	fp.syncProxyRules()

	checkBind, err := fp.hasBindIP("10.20.30.41")
	if !checkBind{
		t.Errorf("failed find bind service %v", err)
	}
	svcNumber, err := fp.checkSvcNumber()
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != 3{
		t.Errorf("svcNumber: %d, expected: 3", svcNumber)
	}

	endpointNumber, err := fp.checkEndpointNumber("10.20.30.41",80)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("svcNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err := fp.hasRuleDestination("10.20.30.41",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}

	endpointNumber, err = fp.checkEndpointNumber("50.60.70.80",80)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("svcNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err = fp.hasRuleDestination("50.60.70.80",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}

	endpointNumber, err = fp.checkEndpointNumber("50.60.70.81",80)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("svcNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err = fp.hasRuleDestination("50.60.70.81",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}
}

func TestClusterIPBindUnBind(t *testing.T){
	fp := NewFakeProxier()

	services := []*api.Service{
		makeTestService("ns1", "svc1", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.4"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test00", "TCP", 1234, 0, 0)
		}),
		makeTestService("ns2", "svc2", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.4"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test10", "TCP", 345, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test11", "TCP", 346, 0, 0)
		}),
		makeTestService("ns3", "svc3", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.4"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test20", "TCP", 347, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test21", "TCP", 348, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test22", "TCP", 349, 0, 0)
		}),
		makeTestService("ns4", "svc4", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.7"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test30", "TCP", 2234, 0, 0)
		}),
		makeTestService("ns5", "svc5", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.7"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test40", "TCP", 3234, 0, 0)
		}),
		makeTestService("ns6", "svc6", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.8"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test50", "TCP", 3234, 0, 0)
		}),
	}

	clusterIPBindUnBindTest(t, fp, 1, services[0])
	clusterIPBindUnBindTest(t, fp, 1, services[0], services[1])
	clusterIPBindUnBindTest(t, fp, 1, services[0], services[1], services[2])
	clusterIPBindUnBindTest(t, fp, 2, services[0], services[1], services[2], services[3])
	clusterIPBindUnBindTest(t, fp, 2, services[0], services[2], services[3])
	clusterIPBindUnBindTest(t, fp, 2, services[0], services[1], services[2], services[3], services[4])
	clusterIPBindUnBindTest(t, fp, 3, services[0], services[1], services[2], services[3], services[4], services[5])

	clusterIPHasBind(t, fp, "172.16.55.4")
	clusterIPHasBind(t, fp, "172.16.55.8")
	clusterIPHasBind(t, fp, "172.16.55.7")

	clusterIPBindUnBindTest(t, fp, 3, services[1], services[2], services[3], services[4], services[5])
	clusterIPBindUnBindTest(t, fp, 3, services[2], services[3], services[5])
	clusterIPBindUnBindTest(t, fp, 2, services[0], services[1], services[2], services[5])

	clusterIPHasBind(t, fp, "172.16.55.4")
	clusterIPHasBind(t, fp, "172.16.55.8")
	clusterIPHasNotBind(t, fp, "172.16.55.7")
}

func TestServiceAddRemove(t *testing.T){

	fp := NewFakeProxier()

	services := []*api.Service{
		makeTestService("ns1", "svc1", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.4"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test00", "TCP", 1234, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test01", "TCP", 1235, 0, 0)
		}),
		makeTestService("ns2", "svc2", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.5"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test10", "TCP", 345, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test11", "TCP", 346, 0, 0)
		}),
		makeTestService("ns3", "svc3", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.6"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test20", "TCP", 347, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test21", "TCP", 348, 0, 0)
		}),
		makeTestService("ns4", "svc4", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.7"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test30", "TCP", 2234, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test31", "TCP", 2235, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test32", "TCP", 2236, 0, 0)
		}),
		makeTestService("ns5", "svc5", func(svc *api.Service) {
			svc.Spec.Type = api.ServiceTypeClusterIP
			svc.Spec.ClusterIP = "172.16.55.8"
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test40", "TCP", 3234, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test41", "TCP", 3235, 0, 0)
			svc.Spec.Ports = addTestPort(svc.Spec.Ports, "test42", "TCP", 3236, 0, 0)
		}),
	}


	serviceAddRemoveTest(t, fp, 4,  services[0],services[1])
	serviceAddRemoveTest(t, fp, 6,  services[0],services[1],services[2])
	serviceAddRemoveTest(t, fp, 7,  services[0],services[1],services[3])
	serviceAddRemoveTest(t, fp, 9,  services[0],services[1],services[2],services[3])
	serviceAddRemoveTest(t, fp, 12, services[0],services[1],services[2],services[3],services[4])
	serviceAddRemoveTest(t, fp, 12, services[0],services[1],services[2],services[3],services[4])
	serviceAddRemoveTest(t, fp, 10, services[0],services[2],services[3],services[4])
	serviceAddRemoveTest(t, fp, 3,  services[4])
	serviceAddRemoveTest(t, fp, 12, services[0],services[1],services[2],services[3],services[4])
	serviceAddRemoveTest(t, fp, 10, services[1],services[2],services[3],services[4])

	serviceHasNotRule(t, fp, "172.16.55.4", 1234)
	serviceHasNotRule(t, fp, "172.16.55.4", 1235)
	serviceHasRule(t, fp, "172.16.55.5", 345)
	serviceHasRule(t, fp, "172.16.55.5", 346)
	serviceHasRule(t, fp, "172.16.55.6", 347)
	serviceHasRule(t, fp, "172.16.55.6", 348)
	serviceHasRule(t, fp, "172.16.55.7", 2234)
	serviceHasRule(t, fp, "172.16.55.7", 2235)
	serviceHasRule(t, fp, "172.16.55.7", 2236)
	serviceHasRule(t, fp, "172.16.55.8", 3236)
	serviceHasRule(t, fp, "172.16.55.8", 3236)
	serviceHasRule(t, fp, "172.16.55.8", 3236)

}

func TestEndpointAddRemove (t *testing.T){

	fp := NewFakeProxier()

	svcPortName := proxy.ServicePortName{
		NamespacedName: makeNSN("ns1", "svc1"),
		Port:           "p80",
	}

	svcIP := "10.20.30.41"
	svcPort := 80

	makeServiceMap(fp,
		makeTestService(svcPortName.Namespace, svcPortName.Name, func(svc *api.Service) {
			svc.Spec.Type = "ClusterIP"
			svc.Spec.ClusterIP = svcIP
			svc.Spec.Ports = []api.ServicePort{{
				Name:     svcPortName.Port,
				Port:     int32(svcPort),
				Protocol: api.ProtocolTCP,
			}}
		}),
	)

	eps := []struct{
		ip      string
		port    int
	}{
		{"1.1.1.1", 11},
		{"1.1.1.2", 12},
		{"1.1.1.3", 13},
		{"1.1.1.4", 14},
		{"1.1.2.1", 21},
		{"1.1.2.2", 22},
		{"1.1.2.3", 23},
		{"1.1.2.4", 24},
	}

	endpoints := []*api.Endpoints{
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[0].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[0].port),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[1].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[1].port),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[2].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[2].port),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[3].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[3].port),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[4].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[4].port),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[5].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[5].port),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[6].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[6].port),
				}},
			}}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{{
				Addresses: []api.EndpointAddress{{
					IP: eps[7].ip,
				}},
				Ports: []api.EndpointPort{{
					Name: svcPortName.Port,
					Port: int32(eps[7].port),
				}},
			}}
		}),
	}

	endpointAddRemoveTest(t, fp, svcIP, svcPort, 1, endpoints[0])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 2, endpoints[0], endpoints[1])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 4, endpoints[0], endpoints[1], endpoints[2], endpoints[3])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 8, endpoints[0], endpoints[1], endpoints[2], endpoints[3], endpoints[4], endpoints[5], endpoints[6], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 6, endpoints[0], endpoints[3], endpoints[4], endpoints[5], endpoints[6], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 6, endpoints[0], endpoints[1], endpoints[3], endpoints[4], endpoints[5], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 8, endpoints[0], endpoints[1], endpoints[2], endpoints[3], endpoints[4], endpoints[5], endpoints[6], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 7, endpoints[0], endpoints[1], endpoints[2], endpoints[3], endpoints[4], endpoints[5], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 8, endpoints[0], endpoints[1], endpoints[2], endpoints[3], endpoints[4], endpoints[5], endpoints[6], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 4, endpoints[1], endpoints[2], endpoints[3], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 5, endpoints[0], endpoints[1], endpoints[2], endpoints[3], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 8, endpoints[0], endpoints[1], endpoints[2], endpoints[3], endpoints[4], endpoints[5], endpoints[6], endpoints[7])
	endpointAddRemoveTest(t, fp, svcIP, svcPort, 4, endpoints[2], endpoints[3], endpoints[5], endpoints[6])

	endpointHasRule(t, fp, svcIP, svcPort, eps[2].ip, eps[2].port)
	endpointHasRule(t, fp, svcIP, svcPort, eps[3].ip, eps[3].port)
	endpointHasRule(t, fp, svcIP, svcPort, eps[5].ip, eps[5].port)
	endpointHasRule(t, fp, svcIP, svcPort, eps[6].ip, eps[6].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[0].ip, eps[0].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[1].ip, eps[1].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[4].ip, eps[4].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[7].ip, eps[7].port)
}

func clusterIPBindUnBindTest(t *testing.T, fp *Proxier, expected int, allServices ...*api.Service){
	fp.serviceMap = make(util.ProxyServiceMap)
	makeServiceMap(fp,allServices...)
	fp.syncProxyRules()
	bindNumber, err := fp.checkBindNumber()
	if err != nil{
		t.Errorf("checkBindNumer failer: %v", err)
	}else if bindNumber != expected{
		t.Errorf("svcNumber: %d, expected: %d", bindNumber, expected)
	}
}

func clusterIPHasBind(t *testing.T, fp *Proxier, ip string){
	check, err := fp.hasBindIP(ip)
	if err != nil{
		t.Errorf("failed find bind svc %v", err)
		return
	}
	if !check{
		t.Errorf("failed find bind svc %v", err)
		return
	}
}

func clusterIPHasNotBind(t *testing.T, fp *Proxier, ip string){
	check, err := fp.hasNotBindIP(ip)
	if err != nil{
		t.Errorf("failed find bind svc %v", err)
		return
	}
	if !check{
		t.Errorf("failed find bind svc %v", err)
		return
	}
}

func serviceAddRemoveTest(t *testing.T, fp *Proxier, expected int, allServices ...*api.Service){
	fp.serviceMap = make(util.ProxyServiceMap)
	makeServiceMap(fp,allServices...)
	fp.syncProxyRules()
	svcNumber, err := fp.checkSvcNumber()
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != expected{
		t.Errorf("svcNumber: %d, expected: %d", svcNumber, expected)
	}

}

func serviceHasRule(t *testing.T, fp *Proxier, svcIP string, svcPort int){

	bind, err := fp.hasBindIP(svcIP)
	if err != nil{
		t.Errorf("failed find bind svc %v", err)
		return
	}
	if !bind{
		t.Errorf("failed find bind svc %v", err)
		return
	}
	check, err := fp.hasRuleService(svcIP,svcPort)
	if err != nil{
		t.Errorf("failed find rule %v", err)
		return
	}
	if !check{
		t.Errorf("failed find rule %v", err)
		return
	}
}

func serviceHasNotRule(t *testing.T, fp *Proxier, svcIP string, svcPort int) {
	check, err := fp.hasNotRuleService(svcIP,svcPort)
	if err != nil{
		t.Errorf("failed find rule %v", err)
		return
	}
	if !check{
		t.Errorf("failed find rule %v", err)
		return
	}
}

func endpointAddRemoveTest(t *testing.T, fp *Proxier, svcIP string, svcPort int, expected int, allEndpoints ...*api.Endpoints){
	fp.endpointsMap = make(util.ProxyEndpointMap)
	makeEndpointsMap(fp, allEndpoints...)
	fp.syncProxyRules()
	epNumber, err := fp.checkEndpointNumber(svcIP, svcPort)
	if err != nil{
		t.Errorf("checkEpNumer failed: %v", err)
	}else if epNumber != expected{
		t.Errorf("epNumber: %d, expected: %d", epNumber, expected)
	}

}

func endpointHasRule(t *testing.T, fp *Proxier, svcIP string, svcPort int, epIP string, epPort int){
	check, err := fp.hasRuleDestination(svcIP, svcPort, epIP, epPort)
	if err != nil{
		t.Errorf("failed find rule %v", err)
		return
	}
	if !check{
		t.Errorf("failed find rule %v", err)
		return
	}
}

func endpointHasNotRule(t *testing.T, fp *Proxier, svcIP string, svcPort int, epIP string, epPort int){
	check, err := fp.hasNotRuleDestination(svcIP, svcPort, epIP, epPort)
	if err != nil{
		t.Errorf("failed find rule %v", err)
		return
	}
	if !check{
		t.Errorf("failed find rule %v", err)
		return
	}
}

func addTestPort(array []api.ServicePort, name string, protocol api.Protocol, port, nodeport int32, targetPort int) []api.ServicePort {
	svcPort := api.ServicePort{
		Name:       name,
		Protocol:   protocol,
		Port:       port,
		NodePort:   nodeport,
		TargetPort: intstr.FromInt(targetPort),
	}
	return append(array, svcPort)
}

func (fp *Proxier) checkBindNumber() (int, error){
	bindIPs, err := fp.ipvsInterface.ListDuumyClusterIp(nil)
	if err != nil{
		return 0, err
	}
	return len(bindIPs), nil
}

func (fp *Proxier) hasBindIP(svcIP string) (bool, error){
	addrs, err := fp.ipvsInterface.ListDuumyClusterIp(nil)
	if err != nil{
		return false, err
	}
	for _, addr := range addrs{
		if strings.Compare(addr.IP.String(),svcIP) == 0{
			return true, nil
		}
	}
	return false, fmt.Errorf("cannot find bind service %s", svcIP)
}

func (fp *Proxier) hasNotBindIP(svcIP string) (bool, error){
	addrs, err := fp.ipvsInterface.ListDuumyClusterIp(nil)
	if err != nil{
		return false, err
	}
	for _, addr := range addrs{
		if strings.Compare(addr.IP.String(),svcIP) == 0 {
			return false, fmt.Errorf("find unexpect bind ip %s", svcIP)
		}
	}
	return true, nil
}

func (fp *Proxier) checkSvcNumber() (int, error){
	ipvsServices, err := fp.ipvsInterface.ListIpvsService()
	if err != nil{
		return 0, err
	}
	return len(ipvsServices), nil
}

func (fp *Proxier) checkEndpointNumber (svcIP string, svcPort int) (int, error){
	ipvsServices, err := fp.ipvsInterface.ListIpvsService()
	if err != nil{
		return 0, err
	}
	for _, ipvsService := range ipvsServices {
		if strings.Compare(ipvsService.Address.String(), svcIP) == 0 && ipvsService.Port == uint16(svcPort) {
			ipvsDestinations, err := fp.ipvsInterface.ListIpvsServer(ipvsService)
			if err != nil {
				return 0, err
			}
			return len(ipvsDestinations), nil
		}
	}
	return 0, fmt.Errorf("can not find svc %s:%d", svcIP, svcPort)
}

func (fp *Proxier) hasRuleService (svcIP string, svcPort int) (bool, error){
	ipvsServices, err := fp.ipvsInterface.ListIpvsService()
	if err != nil{
		return false, err
	}
	for _, ipvsService := range ipvsServices{
		if strings.Compare(ipvsService.Address.String(),svcIP) == 0 && ipvsService.Port == uint16(svcPort) {
			return true, nil
		}
	}
	return  false, fmt.Errorf("cannot find service %s:%d", svcIP, svcPort)
}

func (fp *Proxier) hasNotRuleService (svcIP string, svcPort int) (bool, error){
	ipvsServices, err := fp.ipvsInterface.ListIpvsService()
	if err != nil{
		return false, err
	}
	for _, ipvsService := range ipvsServices{
		if strings.Compare(ipvsService.Address.String(),svcIP) == 0 && ipvsService.Port == uint16(svcPort) {
			return false, fmt.Errorf("find unexpect service %s:%d", svcIP, svcPort)
		}
	}
	return true, nil
}

func (fp *Proxier) hasRuleDestination (svcIP string, svcPort int, epIP string, epPort int) (bool, error){
	ipvsServices, err := fp.ipvsInterface.ListIpvsService()
	if err != nil{
		return false, err
	}
	for _, ipvsService := range ipvsServices{
		if strings.Compare(ipvsService.Address.String(),svcIP) == 0 && ipvsService.Port == uint16(svcPort){
			ipvsDestinations, err := fp.ipvsInterface.ListIpvsServer(ipvsService)
			if err != nil{
				return false, err
			}
			for _, ipvsDestination := range ipvsDestinations{

				if strings.Compare(ipvsDestination.Ip,epIP) == 0 && ipvsDestination.Port == epPort{
					return true, nil
				}
			}
			return false, fmt.Errorf("cannot find endpoint %s:%d in service", epIP, epPort)
		}
	}
	return  false, fmt.Errorf("cannot find service %s:%d", svcIP, svcPort)
}

func (fp *Proxier) hasNotRuleDestination (svcIP string, svcPort int, epIP string, epPort int) (bool, error){
	ipvsServices, err := fp.ipvsInterface.ListIpvsService()
	if err != nil{
		return false, err
	}
	for _, ipvsService := range ipvsServices{
		if strings.Compare(ipvsService.Address.String(),svcIP) == 0 && ipvsService.Port == uint16(svcPort){
			ipvsDestinations, err := fp.ipvsInterface.ListIpvsServer(ipvsService)
			if err != nil{
				return false, err
			}
			for _, ipvsDestination := range ipvsDestinations{
				if strings.Compare(ipvsDestination.Ip,epIP) == 0 && ipvsDestination.Port == epPort{
					return false, fmt.Errorf("find unexpected endpoint %s:%d", epIP, epPort)
				}
			}
			return true, nil
		}
	}
	return  false, fmt.Errorf("cannot find service %s:%d", svcIP, svcPort)
}