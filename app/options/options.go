package options

import (

	"github.com/spf13/pflag"
)

type KubeEnnProxyConfig struct {

}


func NewKubeEnnProxyConfig() *KubeEnnProxyConfig {
	return &KubeEnnProxyConfig{

	}
}

func (s *KubeEnnProxyConfig) AddFlags(fs *pflag.FlagSet) {

}