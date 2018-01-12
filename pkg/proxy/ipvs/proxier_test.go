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

type fakeNodeIP struct{}

func (f *fakeNodeIP) GetNodeIPs(ipvs utilipvs.Interface) ([]net.IP, error){
	ips := []net.IP{net.ParseIP("100.101.102.103")}
	return ips, nil
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
		proxier.OnServiceAdd(allServices[i])
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
		proxier.OnEndpointsAdd(allEndpoints[i])
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
		serviceChanges:    util.NewServiceChangeMap(),
		endpointsChanges:  util.NewEndpointsChangeMap(testHostname),
		serviceMap:        make(util.ProxyServiceMap),
		endpointsMap:      make(util.ProxyEndpointMap),
		portsMap:          make(map[util.LocalPort]util.Closeable),
		kernelIpvsMap:     make(map[activeServiceKey][]activeServiceValue),
		kernelClusterMap:  make(map[string]int),
		clusterCIDR:       "10.0.0.0/24",
		hostname:          testHostname,
		nodeIPs:           nodeIPs,
		nodeIPInterface:   &fakeNodeIP{},
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
			ept.Subsets = []api.EndpointSubset{
				{
					Addresses: []api.EndpointAddress{{
						IP: epIP1,
					}},
					Ports: []api.EndpointPort{{
						Name: svcPortName.Port,
						Port: int32(epPort1),
					}},
				},
				{
					Addresses: []api.EndpointAddress{{
						IP: epIP2,
					}},
					Ports: []api.EndpointPort{{
						Name: svcPortName.Port,
						Port: int32(epPort2),
					}},
				},
			}
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
		t.Errorf("endpointNumber: %d, expected: 2", endpointNumber)
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
		t.Errorf("endpointNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err := fp.hasRuleDestination("10.20.30.41",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}

	endpointNumber, err = fp.checkEndpointNumber("100.101.102.103",30001)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("endpointNumber: %d, expected: 1", endpointNumber)
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
		t.Errorf("endpointNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err := fp.hasRuleDestination("10.20.30.41",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}

	endpointNumber, err = fp.checkEndpointNumber("50.60.70.80",80)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("endpointNumber: %d, expected: 1", endpointNumber)
	}
	checkRule, err = fp.hasRuleDestination("50.60.70.80",80,"10.180.0.1",80)
	if !checkRule{
		t.Errorf("failed find rule %v", err)
	}

	endpointNumber, err = fp.checkEndpointNumber("50.60.70.81",80)
	if err != nil{
		t.Errorf("checkEndpointNumber failed: %v", err)
	}else if endpointNumber != 1{
		t.Errorf("endpointNumber: %d, expected: 1", endpointNumber)
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

	fp.endpointsSynced = true
	fp.servicesSynced  = true

	fp.OnServiceAdd(services[0])
	clusterIPBindUnBindTest(t, 0, fp, 1)

	fp.OnServiceAdd(services[1])
	clusterIPBindUnBindTest(t, 1, fp, 1)

	fp.OnServiceAdd(services[2])
	clusterIPBindUnBindTest(t, 2, fp, 1)

	fp.OnServiceAdd(services[3])
	clusterIPBindUnBindTest(t, 3, fp, 2)

	fp.OnServiceDelete(services[1])
	clusterIPBindUnBindTest(t, 4, fp, 2)

	fp.OnServiceAdd(services[4])
	clusterIPBindUnBindTest(t, 5, fp, 2)

	fp.OnServiceAdd(services[5])
	clusterIPBindUnBindTest(t, 6, fp, 3)

	fp.OnServiceAdd(services[1])
	clusterIPBindUnBindTest(t, 7, fp, 3)

	clusterIPHasBind(t, fp, "172.16.55.4")
	clusterIPHasBind(t, fp, "172.16.55.8")
	clusterIPHasBind(t, fp, "172.16.55.7")

	fp.OnServiceDelete(services[0])
	fp.OnServiceDelete(services[1])
	clusterIPBindUnBindTest(t, 8, fp, 3)

	fp.OnServiceDelete(services[4])
	clusterIPBindUnBindTest(t, 9, fp, 3)

	fp.OnServiceDelete(services[3])
	fp.OnServiceAdd(services[0])
	fp.OnServiceAdd(services[1])
	clusterIPBindUnBindTest(t, 10, fp, 2)

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

	fp.endpointsSynced = true
	fp.servicesSynced  = true

	fp.OnServiceAdd(services[0])
	fp.OnServiceAdd(services[1])
	serviceAddRemoveTest(t, 0, fp, 4)

	fp.OnServiceAdd(services[2])
	serviceAddRemoveTest(t, 1, fp, 6)

	fp.OnServiceAdd(services[3])
	fp.OnServiceDelete(services[2])
	serviceAddRemoveTest(t, 2, fp, 7)

	fp.OnServiceAdd(services[2])
	serviceAddRemoveTest(t, 3, fp, 9)

	fp.OnServiceAdd(services[4])
	serviceAddRemoveTest(t, 4, fp, 12)

	fp.OnServiceDelete(services[1])
	serviceAddRemoveTest(t, 5, fp, 10)

	fp.OnServiceDelete(services[0])
	fp.OnServiceDelete(services[2])
	fp.OnServiceDelete(services[3])
	serviceAddRemoveTest(t, 6, fp, 3)

	fp.OnServiceAdd(services[0])
	fp.OnServiceAdd(services[1])
	fp.OnServiceAdd(services[2])
	fp.OnServiceAdd(services[3])
	serviceAddRemoveTest(t, 7, fp, 12)

	fp.OnServiceDelete(services[0])
	serviceAddRemoveTest(t, 8, fp, 10)

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

func TestBuildServiceMapServiceUpdate(t *testing.T) {

	fp := NewFakeProxier()

	servicev1 := makeTestService("somewhere", "some-service", func(svc *api.Service) {
		svc.Spec.Type = api.ServiceTypeClusterIP
		svc.Spec.ClusterIP = "100.16.55.4"
		svc.Spec.Ports = addTestPort(svc.Spec.Ports, "something", "TCP", 1234, 0, 0)
		svc.Spec.Ports = addTestPort(svc.Spec.Ports, "somethingelse", "TCP", 1235, 0, 0)
	})
	servicev2 := makeTestService("somewhere", "some-service", func(svc *api.Service) {
		svc.Spec.Type = api.ServiceTypeClusterIP
		svc.Spec.ClusterIP = "100.16.55.4"
		svc.Spec.Ports = addTestPort(svc.Spec.Ports, "something1", "TCP", 1234, 0, 0)
		svc.Spec.Ports = addTestPort(svc.Spec.Ports, "something2", "TCP", 1235, 0, 0)
		svc.Spec.Ports = addTestPort(svc.Spec.Ports, "something3", "TCP", 1236, 0, 0)
	})

	servicev3 := makeTestService("somewhere", "some-service", func(svc *api.Service) {
		svc.Spec.Type = api.ServiceTypeNodePort
		svc.Spec.ClusterIP = "100.16.55.4"
		svc.Spec.Ports = addTestPort(svc.Spec.Ports, "something", "TCP", 1234, 4321, 0)
		svc.Spec.Ports = addTestPort(svc.Spec.Ports, "somethingelse", "TCP", 1235, 5321, 0)
	})

	fp.endpointsSynced = true
	fp.servicesSynced  = true

	fp.OnServiceAdd(servicev1)
	fp.syncProxyRules()
	if len(fp.serviceMap) != 2 {
		t.Errorf("expected service map length 2, got %v", fp.serviceMap)
	}
	svcNumber, err := fp.checkSvcNumber()
	expected := 2
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != expected{
		t.Errorf("svcNumber: %d, expected: %d", svcNumber, expected)
	}

	fp.OnServiceUpdate(servicev1, servicev2)
	fp.syncProxyRules()
	if len(fp.serviceMap) != 3 {
		t.Errorf("expected service map length 2, got %v", fp.serviceMap)
	}
	svcNumber, err = fp.checkSvcNumber()
	expected = 3
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != expected{
		t.Errorf("svcNumber: %d, expected: %d", svcNumber, expected)
	}

	fp.OnServiceUpdate(servicev2, servicev2)
	fp.syncProxyRules()
	if len(fp.serviceMap) != 3 {
		t.Errorf("expected service map length 2, got %v", fp.serviceMap)
	}
	svcNumber, err = fp.checkSvcNumber()
	expected = 3
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != expected{
		t.Errorf("svcNumber: %d, expected: %d", svcNumber, expected)
	}

	fp.OnServiceUpdate(servicev2, servicev3)
	fp.syncProxyRules()
	if len(fp.serviceMap) != 2 {
		t.Errorf("expected service map length 2, got %v", fp.serviceMap)
	}
	svcNumber, err = fp.checkSvcNumber()
	expected = 4
	if err != nil{
		t.Errorf("checkSvcNumer failed: %v", err)
	}else if svcNumber != expected{
		t.Errorf("svcNumber: %d, expected: %d", svcNumber, expected)
	}

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

	endPointSub := []api.EndpointSubset{
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[0].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[0].port),
			}},
		},
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[1].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[1].port),
			}},
		},
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[2].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[2].port),
			}},
		},
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[3].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[3].port),
			}},
		},
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[4].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[4].port),
			}},
		},
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[5].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[5].port),
			}},
		},
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[6].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[6].port),
			}},
		},
		{
			Addresses: []api.EndpointAddress{{
				IP: eps[7].ip,
			}},
			Ports: []api.EndpointPort{{
				Name: svcPortName.Port,
				Port: int32(eps[7].port),
			}},
		},
	}

	endpoints := []*api.Endpoints{
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
				endPointSub[1],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
				endPointSub[1],
				endPointSub[2],
				endPointSub[3],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
				endPointSub[1],
				endPointSub[2],
				endPointSub[3],
				endPointSub[4],
				endPointSub[5],
				endPointSub[6],
				endPointSub[7],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
				endPointSub[3],
				endPointSub[4],
				endPointSub[5],
				endPointSub[6],
				endPointSub[7],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
				endPointSub[1],
				endPointSub[3],
				endPointSub[4],
				endPointSub[5],
				endPointSub[7],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
				endPointSub[1],
				endPointSub[2],
				endPointSub[3],
				endPointSub[4],
				endPointSub[5],
				endPointSub[7],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[1],
				endPointSub[2],
				endPointSub[3],
				endPointSub[7],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[0],
				endPointSub[1],
				endPointSub[2],
				endPointSub[3],
				endPointSub[7],
			}
		}),
		makeTestEndpoints(svcPortName.Namespace, svcPortName.Name, func(ept *api.Endpoints) {
			ept.Subsets = []api.EndpointSubset{
				endPointSub[2],
				endPointSub[3],
				endPointSub[5],
				endPointSub[6],
			}
		}),
	}

	fp.endpointsSynced = true
	fp.servicesSynced  = true

	fp.OnEndpointsAdd(endpoints[0])
	endpointAddRemoveTest(t, 0,  fp, svcIP, svcPort, 1)

	fp.OnEndpointsAdd(endpoints[1])
	endpointAddRemoveTest(t, 1,  fp, svcIP, svcPort, 2)

	fp.OnEndpointsUpdate(endpoints[1],endpoints[2])
	endpointAddRemoveTest(t, 2,  fp, svcIP, svcPort, 4)

	fp.OnEndpointsUpdate(endpoints[2],endpoints[3])
	endpointAddRemoveTest(t, 3,  fp, svcIP, svcPort, 8)

	fp.OnEndpointsUpdate(endpoints[3],endpoints[4])
	endpointAddRemoveTest(t, 4,  fp, svcIP, svcPort, 6)

	fp.OnEndpointsUpdate(endpoints[4],endpoints[5])
	endpointAddRemoveTest(t, 5,  fp, svcIP, svcPort, 6)

	fp.OnEndpointsUpdate(endpoints[5],endpoints[3])
	endpointAddRemoveTest(t, 6,  fp, svcIP, svcPort, 8)

	fp.OnEndpointsUpdate(endpoints[3],endpoints[6])
	endpointAddRemoveTest(t, 7,  fp, svcIP, svcPort, 7)

	fp.OnEndpointsUpdate(endpoints[6],endpoints[7])
	endpointAddRemoveTest(t, 8,  fp, svcIP, svcPort, 4)

	fp.OnEndpointsUpdate(endpoints[7],endpoints[8])
	endpointAddRemoveTest(t, 9,  fp, svcIP, svcPort, 5)

	fp.OnEndpointsDelete(endpoints[8])
	endpointAddRemoveTest(t, 10, fp, svcIP, svcPort, 0)

	fp.OnEndpointsAdd(endpoints[3])
	endpointAddRemoveTest(t, 11, fp, svcIP, svcPort, 8)

	fp.OnEndpointsUpdate(endpoints[3],endpoints[9])
	endpointAddRemoveTest(t, 12, fp, svcIP, svcPort, 4)

	endpointHasRule(t, fp, svcIP, svcPort, eps[2].ip, eps[2].port)
	endpointHasRule(t, fp, svcIP, svcPort, eps[3].ip, eps[3].port)
	endpointHasRule(t, fp, svcIP, svcPort, eps[5].ip, eps[5].port)
	endpointHasRule(t, fp, svcIP, svcPort, eps[6].ip, eps[6].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[0].ip, eps[0].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[1].ip, eps[1].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[4].ip, eps[4].port)
	endpointHasNotRule(t, fp, svcIP, svcPort, eps[7].ip, eps[7].port)
}

func clusterIPBindUnBindTest(t *testing.T, caseNumber int, fp *Proxier, expected int){

	fp.syncProxyRules()
	// todo: double sync because unbind cluster ip can be done only after reset serviceChangeMap and endpointChangeMap
	fp.syncProxyRules()
	bindNumber, err := fp.checkBindNumber()
	if err != nil{
		t.Errorf("caseNumber: %d, checkBindNumer failer: %v", caseNumber, err)
	}else if bindNumber != expected{
		t.Errorf("caseNumber: %d, svcNumber: %d, expected: %d", caseNumber, bindNumber, expected)
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

func serviceAddRemoveTest(t *testing.T, caseNumber int, fp *Proxier, expected int){

	fp.syncProxyRules()
	svcNumber, err := fp.checkSvcNumber()
	if err != nil{
		t.Errorf("caseNumber: %d, checkSvcNumer failed: %v", caseNumber, err)
	}else if svcNumber != expected{
		t.Errorf("caseNumber: %d, svcNumber: %d, expected: %d", caseNumber, svcNumber, expected)
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

func endpointAddRemoveTest(t *testing.T, caseNumber int, fp *Proxier, svcIP string, svcPort int, expected int){

	fp.syncProxyRules()
	epNumber, err := fp.checkEndpointNumber(svcIP, svcPort)
	if err != nil{
		t.Errorf("caseNumber: %d, checkEpNumer failed: %v", caseNumber, err)
	}else if epNumber != expected{
		t.Errorf("caseNumber: %d, epNumber: %d, expected: %d", caseNumber, epNumber, expected)
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