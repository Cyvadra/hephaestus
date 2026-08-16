.PHONY: build build-server build-frontend deploy-build run test test-integration vet swagger

build:
	go build ./...

build-server:
	go build -o hephaestus ./cmd/hephaestus

build-frontend:
	npm --prefix frontend ci
	npm --prefix frontend run build

deploy-build: build-server build-frontend

run:
	go run ./cmd/hephaestus

vet:
	go vet ./...

test:
	go test ./...

# Requires HEPHAESTUS_TEST_POSTGRES_DSN to point at a real Postgres
# instance; integration tests are skipped otherwise.
test-integration:
	go test ./... -run TestIntegration -v

# Regenerates docs/swagger from the @-annotations in internal/server and
# cmd/hephaestus/main.go. Requires the swag CLI (go install
# github.com/swaggo/swag/cmd/swag@latest).
swagger:
	swag init -g cmd/hephaestus/main.go -o docs/swagger --parseDependency --parseInternal
