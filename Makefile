all: docker

docker: build
    # sudo docker build -t "xujieasd/kube-enn-proxy" .

build:
	@echo "kube-enn-proxy binary build Starting."
	CGO_ENABLED=0 go build -o kube-enn-proxy kube-enn-proxy.go
	@echo "kube-enn-proxy binary build finished."

test:
	@echo "kube-enn-proxy unit test Starting."
	hack/test.sh
	@echo "kube-enn-proxy unit test finished."

clean:
	rm -f kube-enn-proxy