.PHONY: build run test test-integration vet swagger

build:
	go build ./...

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
