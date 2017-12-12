package options

import (
	"time"

	"github.com/spf13/pflag"
	"github.com/mqliang/libipvs"
)

type KubeEnnProxyConfig struct {
	Kubeconfig         string
	Master             string
	ConfigSyncPeriod   time.Duration
	CleanupConfig      bool
	IPTablesSyncPeriod time.Duration
	IpvsSyncPeriod     time.Duration
	MinSyncPeriod      time.Duration

	MasqueradeAll      bool
	MasqueradeBit      int32
	ClusterCIDR        string
	HostnameOverride   string

	IpvsScheduler      string

	GlogToStderr       bool
	GlogV              string
	GlogDir            string

	BindAddress string
}


func NewKubeEnnProxyConfig() *KubeEnnProxyConfig {
	return &KubeEnnProxyConfig{
		ConfigSyncPeriod:   5 * time.Minute,
		IpvsSyncPeriod:     5 * time.Minute,
		IPTablesSyncPeriod: 5 * time.Minute,
		MasqueradeAll:      false,
		IpvsScheduler:      libipvs.RoundRobin,
	}
}

func (s *KubeEnnProxyConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&s.BindAddress, "bind-address", s.BindAddress, "The IP address for the proxy server to serve on (set to 0.0.0.0 for all interfaces)")
	fs.BoolVar(&s.GlogToStderr, "logtostderr", s.GlogToStderr, "If true will log to standard error instead of files")
	fs.StringVar(&s.GlogV, "v", s.GlogV, "Log level for V logs")
	fs.StringVar(&s.GlogDir, "log-dir", s.GlogDir, "If none empty, write log files in this directory")
	fs.StringVar(&s.Master, "master", s.Master, "The address of the Kubernetes API server (overrides any value in kubeconfig)")
	fs.StringVar(&s.Kubeconfig, "kubeconfig", s.Kubeconfig, "Path to kubeconfig file with authorization information (the master location is set by the master flag).")
	fs.BoolVar(&s.CleanupConfig,"cleanup-config",s.CleanupConfig,"If true cleanup all ipvs/iptables/ipaddress rules and exit.")
	fs.DurationVar(&s.ConfigSyncPeriod,"config-sync-period",s.ConfigSyncPeriod,"How often configuration from the apiserver is refreshed.  Must be greater than 0.")
	fs.DurationVar(&s.IPTablesSyncPeriod,"iptable-sync-period",s.IPTablesSyncPeriod,"The maximum interval of how often iptables rules are refreshed (e.g. '5s', '1m', '2h22m').  Must be greater than 0.")
	fs.DurationVar(&s.IpvsSyncPeriod,"ipvs-sync-period",s.IpvsSyncPeriod,"The maximum interval of how often ipvs rules are refreshed (e.g. '5s', '1m', '2h22m').  Must be greater than 0.")
	fs.DurationVar(&s.MinSyncPeriod,"min-sync-period",s.MinSyncPeriod,"The minimum interval of how often the iptables rules can be refreshed as endpoints and services change (e.g. '5s', '1m', '2h22m').")
	fs.BoolVar(&s.MasqueradeAll,"masq-all",s.MasqueradeAll,"If true SNAT everything.")
	fs.Int32Var(&s.MasqueradeBit, "iptables-masquerade-bit", s.MasqueradeBit, "If using the pure iptables proxy, the bit of the fwmark space to mark packets requiring SNAT with.  Must be within the range [0, 31].")
	fs.StringVar(&s.ClusterCIDR,"cluser-CIDR",s.ClusterCIDR,"The CIDR range of pods in the cluster. It is used to bridge traffic coming from outside of the cluster.")
	fs.StringVar(&s.HostnameOverride,"hostname-override",s.HostnameOverride,"If non-empty, will use this string as identification instead of the actual hostname.")
	fs.StringVar(&s.IpvsScheduler,"IPVS-scheduler",s.IpvsScheduler,"ipvs load balancer method, if not set, use roundrobin as default.")
}