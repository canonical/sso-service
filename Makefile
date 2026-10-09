CGO_ENABLED?=0
GOOS?=linux
GO_BIN?=app
GO?=go
GOFLAGS?=-ldflags=-w -ldflags=-s -a -buildvcs
DSN?=postgresql://sso:sso@localhost:5432/sso?sslmode=disable

.EXPORT_ALL_VARIABLES:

# Go related
mocks:
	$(GO) install go.uber.org/mock/mockgen@v0.6.0
	# generate gomocks
	$(GO) generate ./...
.PHONY: mocks

# Unit and integration tests. The integration tests (*_integration_test.go)
# start the containers they need, so a container runtime must be available;
# `make test-unit` leaves them out.
test: mocks vet
	$(GO) test ./... -cover -coverprofile coverage_source.out
	# this will be cached, just needed to the test.json
	$(GO) test ./... -cover -coverprofile coverage_source.out -json > test_source.json
	cat coverage_source.out | grep -v "mock_*" | tee coverage.out
	cat test_source.json | grep -v "mock_*" | tee test.json
.PHONY: test

test-unit: mocks vet
	$(GO) test ./... -short
.PHONY: test-unit

vet:
	$(GO) vet ./...
.PHONY: vet

vendor:
	$(GO) mod vendor
.PHONY: vendor

govulncheck: vendor
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	PATH="$$($(GO) env GOPATH)/bin:$$PATH" govulncheck ./...
.PHONY: govulncheck

build:
	$(GO) build -o $(GO_BIN) ./
.PHONY: build

db-status:
	$(GO) run . migrate --dsn $(DSN) status
.PHONY: db-status

db:
	$(GO) run . migrate --dsn $(DSN) up
.PHONY: db

db-down:
	$(GO) run . migrate --dsn $(DSN) down
.PHONY: db-down
