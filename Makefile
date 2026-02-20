.PHONY: build test test-race vet coverage docker-build integration integration-up integration-down container-test container-test-all ci clean download-novnc

BINARY := qemu-bmc
DOCKER_IMAGE := qemu-bmc
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
COMPOSE := $(shell command -v docker-compose 2>/dev/null || echo "docker compose")
COMPOSE_FILE := integration/docker-compose.yml
NOVNC_VERSION := 1.5.0
NOVNC_DIR := internal/novnc/static

download-novnc:
	mkdir -p $(NOVNC_DIR)
	curl -sL https://github.com/novnc/noVNC/archive/refs/tags/v$(NOVNC_VERSION).tar.gz | \
		tar xz --strip-components=1 -C $(NOVNC_DIR) \
		noVNC-$(NOVNC_VERSION)/vnc.html \
		noVNC-$(NOVNC_VERSION)/vnc_lite.html \
		noVNC-$(NOVNC_VERSION)/core \
		noVNC-$(NOVNC_VERSION)/vendor \
		noVNC-$(NOVNC_VERSION)/app

build: download-novnc
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/qemu-bmc

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

vet:
	go vet ./...

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

docker-build:
	docker build -t $(DOCKER_IMAGE) -f docker/Dockerfile .

integration:
	$(COMPOSE) -f $(COMPOSE_FILE) down -v 2>/dev/null || true
	$(COMPOSE) -f $(COMPOSE_FILE) build
	$(COMPOSE) -f $(COMPOSE_FILE) run --rm test
	$(COMPOSE) -f $(COMPOSE_FILE) down -v

integration-up:
	$(COMPOSE) -f $(COMPOSE_FILE) build bmc
	$(COMPOSE) -f $(COMPOSE_FILE) up -d qemu bmc
	@echo "QEMU + BMC running. Use 'make integration-down' to stop."

integration-down:
	$(COMPOSE) -f $(COMPOSE_FILE) down -v

container-test:
	./tests/run_tests.sh --build quick

container-test-all:
	./tests/run_tests.sh --build all

ci: vet test-race integration

clean:
	rm -f $(BINARY) coverage.out
	$(COMPOSE) -f $(COMPOSE_FILE) down -v --rmi local 2>/dev/null || true
