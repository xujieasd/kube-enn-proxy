/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"fmt"
	"strings"
	"strconv"
	"net"

	"k8s.io/utils/exec"

	api "k8s.io/api/core/v1"

	"github.com/golang/glog"
)

// Utilities for dealing with conntrack

const noConnectionToDelete = "0 flow entries have been deleted"

// IPPart returns just the IP part of an IP or IP:port or endpoint string. If the IP
// part is an IPv6 address enclosed in brackets (e.g. "[fd00:1::5]:9999"),
// then the brackets are stripped as well.
func IPPart(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		// IP address without port
		return s
	}
	// Must be IP:port
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		glog.Errorf("Error parsing '%s': %v", s, err)
		return ""
	}
	// Check if host string is a valid IP address
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	} else {
		glog.Errorf("invalid IP part '%s'", host)
	}
	return ""
}

func IsIPv6(netIP net.IP) bool {
	return netIP != nil && netIP.To4() == nil
}

func IsIPv6String(ip string) bool {
	netIP := net.ParseIP(ip)
	return IsIPv6(netIP)
}

func parametersWithFamily(isIPv6 bool, parameters ...string) []string {
	if isIPv6 {
		parameters = append(parameters, "-f", "ipv6")
	}
	return parameters
}

// ClearUDPConntrackForIP uses the conntrack tool to delete the conntrack entries
// for the UDP connections specified by the given service IP
func ClearUDPConntrackForIP(execer exec.Interface, ip string) error {
	parameters := parametersWithFamily(IsIPv6String(ip), "-D", "--orig-dst", ip, "-p", "udp")
	err := ExecConntrackTool(execer, parameters...)
	if err != nil && !strings.Contains(err.Error(), noConnectionToDelete) {
		// TODO: Better handling for deletion failure. When failure occur, stale udp connection may not get flushed.
		// These stale udp connection will keep black hole traffic. Making this a best effort operation for now, since it
		// is expensive to baby-sit all udp connections to kubernetes services.
		return fmt.Errorf("error deleting connection tracking state for UDP service IP: %s, error: %v", ip, err)
	}
	return nil
}

// ClearUDPConntrackForPeers uses the conntrack tool to delete the conntrack entries
// for the UDP connections specified by the {origin, dest} IP pair.
func ClearUDPConntrackForPeers(execer exec.Interface, origin, dest string) error {
	parameters := parametersWithFamily(IsIPv6String(origin), "-D", "--orig-dst", origin, "--dst-nat", dest, "-p", "udp")
	err := ExecConntrackTool(execer, parameters...)
	if err != nil && !strings.Contains(err.Error(), noConnectionToDelete) {
		// TODO: Better handling for deletion failure. When failure occur, stale udp connection may not get flushed.
		// These stale udp connection will keep black hole traffic. Making this a best effort operation for now, since it
		// is expensive to baby sit all udp connections to kubernetes services.
		return fmt.Errorf("error deleting conntrack entries for UDP peer {%s, %s}, error: %v", origin, dest, err)
	}
	return nil
}

// Clear UDP conntrack for port or all conntrack entries when port equal zero.
// When a packet arrives, it will not go through NAT table again, because it is not "the first" packet.
// The solution is clearing the conntrack. Known issus:
// https://github.com/docker/docker/issues/8795
// https://github.com/kubernetes/kubernetes/issues/31983
func ClearUdpConntrackForPort(execer exec.Interface, port int) {
	glog.V(3).Infof("Deleting conntrack entries for udp connections")
	if port > 0 {
		err := ExecConntrackTool(execer, "-D", "-p", "udp", "--dport", strconv.Itoa(port))
		if err != nil && !strings.Contains(err.Error(), noConnectionToDelete) {
			glog.Errorf("conntrack return with error: %v", err)
		}
	} else {
		glog.Errorf("Wrong port number. The port number must be greater than zero")
	}
}


// After a UDP endpoint has been removed, we must flush any pending conntrack entries to it, or else we
// risk sending more traffic to it, all of which will be lost (because UDP).
// This assumes the proxier mutex is held
func DeleteEndpointConnections(execer exec.Interface, serviceMap ProxyServiceMap, connectionMap map[EndpointServicePair]bool) {
	for epSvcPair := range connectionMap {
		if svcInfo, ok := serviceMap[epSvcPair.ServicePortName]; ok && svcInfo.Protocol == strings.ToLower(string(api.ProtocolUDP)) {
			endpointIP := strings.Split(epSvcPair.Endpoint, ":")[0]
			glog.V(3).Infof("Deleting connection tracking state for service IP %s, endpoint IP %s", svcInfo.ClusterIP.String(), endpointIP)
			err := ExecConntrackTool(execer, "-D", "--orig-dst", svcInfo.ClusterIP.String(), "--dst-nat", endpointIP, "-p", "udp")
			if err != nil && !strings.Contains(err.Error(), noConnectionToDelete) {
				// TODO: Better handling for deletion failure. When failure occur, stale udp connection may not get flushed.
				// These stale udp connection will keep black hole traffic. Making this a best effort operation for now, since it
				// is expensive to baby sit all udp connections to kubernetes services.
				glog.Errorf("conntrack return with error: %v", err)
			}
		}
	}
}

// DeleteServiceConnection uses the conntrack tool to delete the conntrack entries
// for the UDP connections specified by the given service IPs
func DeleteServiceConnections(execer exec.Interface, svcIPs []string) {
	for _, ip := range svcIPs {
		glog.V(3).Infof("Deleting connection tracking state for service IP %s", ip)
		err := ExecConntrackTool(execer, "-D", "--orig-dst", ip, "-p", "udp")
		if err != nil && !strings.Contains(err.Error(), noConnectionToDelete) {
			// TODO: Better handling for deletion failure. When failure occur, stale udp connection may not get flushed.
			// These stale udp connection will keep black hole traffic. Making this a best effort operation for now, since it
			// is expensive to baby-sit all udp connections to kubernetes services.
			glog.Errorf("conntrack returned error: %v", err)
		}
	}
}

// ExecConntrackTool executes the conntrack tool using the given parameters
func ExecConntrackTool(execer exec.Interface, parameters ...string) error {
	conntrackPath, err := execer.LookPath("conntrack")
	if err != nil {
		return fmt.Errorf("error looking for path of conntrack: %v", err)
	}
	output, err := execer.Command(conntrackPath, parameters...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("conntrack command returned: %q, error message: %s", string(output), err)
	}
	return nil
}
