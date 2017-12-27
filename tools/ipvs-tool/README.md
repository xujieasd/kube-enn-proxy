For some reason, some environment or some production policy do not allow me to use ipvsadm, so I need a ipvs-tool tool to debug kube-enn-proxy.
ipvs-tool is a simple tool based on libnetwork to do some base ipvs functions
ipvs-tool outputs referred on ipvsadm outputs

usage:
make
./ipvs-tool -h
```
Usage of ./ipvs-tool:
  -A	add virtual service with options
  -D	delele virtual service
  -a	add real server with options
  -d	delete real servier
  -list
    	list all ipvs rules
  -r string
    	server address is host (adn port) (default "nil")
  -s string
    	one of rr|wrr|lc|wlc|lblc|lblcr|dh|sh|sed|nq, the default is wlc (default "wlc")
  -t string
    	tcp service-address is host[:port] (default "nil")
  -u string
    	udp service-address is host[:port] (default "nil")
```
usage example:
```
./ipvs-tool -list
./ipvs-tool -A -t 10.10.0.1:8080 -s rr
./ipvs-tool -A -t 10.10.0.1:8080 -r 10.10.0.2:8080
./ipvs-tool -d -t 10.10.0.1:8080 -r 10.10.0.3:8080
./ipvs-tool -D -t 10.10.0.1:8080
```


