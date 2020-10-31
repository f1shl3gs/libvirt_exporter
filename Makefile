
build:
	CGO_ENABLED=0 go build -ldflags "-s -w" -o libvirt_exporter cmd/libvirt_exporter/main.go

image: build
	@podman build -t libvirt_exporter -f distribution/docker/Dockerfile .
