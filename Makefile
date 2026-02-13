.PHONY: build test test-race vet coverage docker-build integration integration-up integration-down ci clean

BINARY := qemu-bmc
DOCKER_IMAGE := qemu-bmc
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
COMPOSE := $(shell command -v docker-compose 2>/dev/null || echo "docker compose")
COMPOSE_FILE := integration/docker-compose.yml

build:
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
	docker build -t $(DOCKER_IMAGE) .

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

ci: vet test-race integration

clean:
	rm -f $(BINARY) coverage.out
	$(COMPOSE) -f $(COMPOSE_FILE) down -v --rmi local 2>/dev/null || true
