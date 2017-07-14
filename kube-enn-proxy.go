package main

import (
	//"flag"
	"fmt"
	"os"

	"kube-enn-proxy/app"
	"kube-enn-proxy/app/options"
	"github.com/spf13/pflag"
)

func main() {
	config := options.NewKubeEnnProxyConfig()
	config.AddFlags(pflag.CommandLine)


	s, err := app.NewEnnProxyServerDefault(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Ennproxy config error: %v\n", err)
		os.Exit(1)
	}

	if err = s.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Ennproxy run error: %v\n", err)
		os.Exit(1)
	}


}
