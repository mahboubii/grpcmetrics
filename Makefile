.PHONY: proto
proto:
	protoc  testserver/testserver.proto  \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative 

.PHONY: test
test:
	go test -race -p 1 -v ./...

REMOTE_DEPS = go.mod go.sum

GOLANGCI_VERSION = 1.52.2
GOLANGCI = .bin/golangci/$(GOLANGCI_VERSION)/golangci-lint

$(REMOTE_DEPS):
	go mod download
	go mod tidy

$(GOLANGCI):
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(dir $(GOLANGCI)) v$(GOLANGCI_VERSION)

.PHONY: lint
lint: $(REMOTE_DEPS) $(GOLANGCI)
	$(GOLANGCI) run ./...

.PHONY: lint-fix
lint-fix: $(REMOTE_DEPS) $(GOLANGCI)
	$(GOLANGCI) run --fix ./...
