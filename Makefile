BINARY_NAME=elastic-mcp-server
CLI_NAME=elastic-cli

.PHONY: all build build-cli clean run run-cli

all: build build-cli

build:
	go build -o $(BINARY_NAME) ./cmd/server/main.go

build-cli:
	go build -o $(CLI_NAME) ./cmd/cli/

clean:
	rm -f $(BINARY_NAME) $(CLI_NAME)
	rm -f *.log

run: build
	./$(BINARY_NAME)

run-cli: build build-cli
	./$(CLI_NAME)
