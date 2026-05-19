.PHONY: ui binary mac linux windows all docker docker-push clean

BINARY := ag0
DIST := dist

# Docker tag — override via env or CLI
# Default: ttl.sh (1h expiring registry, no auth needed)
# Examples:
#   make docker                                          → ttl.sh/ag0:1h
#   DOCKER_TAG=docker.io/youruser/ag0:latest make docker
DOCKER_TAG ?= ttl.sh/ag0:1h

# Build the UI once — all binary targets depend on this
ui:
	cd ui && npm install && npm run build

# Native build for current platform
binary: ui
	go build -o $(DIST)/$(BINARY) ./cmd/ag0

# macOS (Apple Silicon + Intel)
mac: ui
	GOOS=darwin GOARCH=arm64 go build -o $(DIST)/$(BINARY)-darwin-arm64 ./cmd/ag0
	GOOS=darwin GOARCH=amd64 go build -o $(DIST)/$(BINARY)-darwin-amd64 ./cmd/ag0

# Linux (x86_64 + arm64)
linux: ui
	GOOS=linux GOARCH=amd64 go build -o $(DIST)/$(BINARY)-linux-amd64 ./cmd/ag0
	GOOS=linux GOARCH=arm64 go build -o $(DIST)/$(BINARY)-linux-arm64 ./cmd/ag0

# Windows (x86_64)
windows: ui
	GOOS=windows GOARCH=amd64 go build -o $(DIST)/$(BINARY)-windows-amd64.exe ./cmd/ag0

# All platforms
all: mac linux windows

# Docker image (uses Dockerfile which does its own UI build)
docker:
	docker build -t $(DOCKER_TAG) .

# Docker build and push
docker-push: docker
	docker push $(DOCKER_TAG)

# Clean build artifacts
clean:
	rm -rf $(DIST) ui/dist