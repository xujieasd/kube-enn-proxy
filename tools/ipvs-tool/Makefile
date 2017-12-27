all: build

build:
	@echo "ipvs-tool binary build Starting."
	CGO_ENABLED=0 go build -o ipvs-tool ipvs-tool.go
	@echo "ipvs-tool binary build finished."

clean:
	rm -f ipvs-tool