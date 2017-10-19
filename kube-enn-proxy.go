package main

import (
	"flag"
	"fmt"
	"os"

	"kube-enn-proxy/app"
	"kube-enn-proxy/app/options"
	"github.com/spf13/pflag"
	"github.com/golang/glog"
)

func main() {
	config := options.NewKubeEnnProxyConfig()
	config.AddFlags(pflag.CommandLine)
	flag.CommandLine.Parse([]string{})

	pflag.Parse()
	defer glog.Flush()

	//flag.Set("logtostderr", "true")

	flag.Set("logtostderr", "false")

	if config.GlogToStderr{
		flag.Set("logtostderr", "true")
	}
	if config.GlogV != "" {
		flag.Set("v",config.GlogV)
	}
	if config.GlogDir != "" {
		flag.Set("log_dir",config.GlogDir)
	}

	if config.CleanupConfig{
		app.CleanUpAndExit()
		os.Exit(0)
	}

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
