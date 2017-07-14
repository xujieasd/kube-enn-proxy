all: build

build:
	@echo "kube-enn-proxy binary build Starting."
	go build -o kube-enn-proxy kube-enn-proxy.go
	@echo "kube-enn-proxy binary build finished."

clean:
	rm -f kube-enn-proxy