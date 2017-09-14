FROM alpine
MAINTAINER xujie xujieasd@gmail.com
RUN apk add --no-cache \
    iptables \
    ipvsadm \
    conntrack-tools \
    curl \
    bash
COPY kube-enn-proxy /

ENTRYPOINT ["/kube-enn-proxy"]