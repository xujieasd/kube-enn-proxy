all: build

build:
	@echo "ipvs-list binary build Starting."
	CGO_ENABLED=0 go build -o ipvs-list ipvs-list.go
	@echo "ipvs-list binary build finished."

clean:
	rm -f ipvs-list