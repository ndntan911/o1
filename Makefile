.PHONY: dev-db dev-db-down test test-integration lint vet run

dev-db:
	docker compose up -d

dev-db-down:
	docker compose down

vet:
	go vet ./...

# Unit tests; runs without Docker.
test:
	go test -short ./...

# All tests; requires `make dev-db` for integration tests.
test-integration:
	go test ./...

lint:
	golangci-lint run

run:
	go run ./cmd/api
