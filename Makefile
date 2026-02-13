.PHONY: build test test-race vet coverage docker-build integration integration-up integration-down ci clean

BINARY := qemu-bmc
DOCKER_IMAGE := qemu-bmc

build:
	go build -o $(BINARY) ./cmd/qemu-bmc

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
	docker compose -f integration/docker-compose.yml run --rm --build test
	docker compose -f integration/docker-compose.yml down -v

integration-up:
	docker compose -f integration/docker-compose.yml up --build -d qemu bmc
	@echo "QEMU + BMC running. Use 'make integration-down' to stop."

integration-down:
	docker compose -f integration/docker-compose.yml down -v

ci: vet test-race integration

clean:
	rm -f $(BINARY) coverage.out
	docker compose -f integration/docker-compose.yml down -v --rmi local 2>/dev/null || true
