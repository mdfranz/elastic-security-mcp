BINARY_NAME=elastic-mcp-server
CLI_NAME=elastic-cli
COMPOSE=podman compose

.PHONY: all build build-cli clean run run-cli redis-up redis-down redis-logs redis-shell

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

redis-up:
	$(COMPOSE) up -d

redis-down:
	$(COMPOSE) down

redis-logs:
	$(COMPOSE) logs -f

redis-shell:
	podman exec -it elastic-security-redis redis-cli
