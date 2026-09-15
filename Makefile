.PHONY: build build-server build-frontend deploy-build deploy run test test-integration vet lint check swagger

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build ./...

build-server:
	go build -ldflags "-X main.version=$(VERSION)" -o hephaestus ./cmd/hephaestus

build-frontend:
	npm --prefix frontend ci
	npm --prefix frontend run build

deploy-build: build-server build-frontend

deploy:
	./scripts/deploy.sh

run:
	go run ./cmd/hephaestus

vet:
	go vet ./...

# Requires golangci-lint v1.64 or newer
# (go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8).
lint:
	golangci-lint run ./...

# The full pre-commit gate.
check: vet lint
	go test -race ./...

test:
	go test ./...

# Requires HEPHAESTUS_TEST_POSTGRES_DSN to point at a real Postgres
# instance; integration tests are skipped otherwise. -p 1 is required:
# every package shares the one database, so migrating it concurrently
# from several test binaries makes them fail against each other.
test-integration:
	go test -p 1 ./... -run TestIntegration -v

# Regenerates docs/swagger from the @-annotations in internal/server and
# cmd/hephaestus/main.go. Requires the swag CLI (go install
# github.com/swaggo/swag/cmd/swag@latest).
swagger:
	swag init -g cmd/hephaestus/main.go -o docs/swagger --parseDependency --parseInternal
