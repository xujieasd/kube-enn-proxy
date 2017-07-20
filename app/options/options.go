package options

import (
	"time"

	"github.com/spf13/pflag"
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
	ClusterCIDR        string
	HostnameOverride   string

	IpvsScheduler      string
}


func NewKubeEnnProxyConfig() *KubeEnnProxyConfig {
	return &KubeEnnProxyConfig{

	}
}

func (s *KubeEnnProxyConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&s.Master, "master", s.Master, "")
	fs.StringVar(&s.Kubeconfig, "kubeconfig", s.Kubeconfig, "")
	fs.BoolVar(&s.CleanupConfig,"cleanup-config",s.CleanupConfig,"")
	fs.DurationVar(&s.ConfigSyncPeriod,"config-sync-period",s.ConfigSyncPeriod,"")
	fs.DurationVar(&s.IPTablesSyncPeriod,"iptable-sync-period",s.IPTablesSyncPeriod,"")
	fs.DurationVar(&s.IpvsSyncPeriod,"ipvs-sync-period",s.IpvsSyncPeriod,"")
	fs.DurationVar(&s.MinSyncPeriod,"min-sync-period",s.MinSyncPeriod,"")
	fs.BoolVar(&s.MasqueradeAll,"masq-all",s.MasqueradeAll,"")
	fs.StringVar(&s.ClusterCIDR,"cluser-CIDR",s.ClusterCIDR,"")
	fs.StringVar(&s.HostnameOverride,"hostname-override",s.HostnameOverride,"")
	fs.StringVar(&s.IpvsScheduler,"IPVS-scheduler",s.IpvsScheduler,"")
}