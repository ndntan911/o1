BACKEND := backend

.PHONY: dev-db dev-db-down test test-integration lint vet run

dev-db:
	docker compose up -d

dev-db-down:
	docker compose down

# Unit tests; runs without Docker.
test:
	cd $(BACKEND) && go test -short ./...

# All tests; requires `make dev-db` for integration tests.
test-integration:
	cd $(BACKEND) && go test ./...

vet:
	cd $(BACKEND) && go vet ./...

lint:
	cd $(BACKEND) && golangci-lint run

run:
	cd $(BACKEND) && go run ./cmd/api
