BINARY_NAME=elastic-mcp-server
CLI_NAME=elastic-cli
COMPOSE=podman compose
TEST_PACKAGES?=./...
GO_TEST_FLAGS?=
GOCACHE?=/tmp/elastic-security-mcp-go-build
PYTHON?=uv run python
SMOKETEST_MODEL?=claude-haiku-4-5
MODELTEST_PROMPT?=list indices

.PHONY: all build build-cli test smoketest modeltest clean run run-cli redis-up redis-down redis-logs redis-shell

all: build build-cli

build:
	go build -o $(BINARY_NAME) ./cmd/server

build-cli:
	go build -o $(CLI_NAME) ./cmd/cli/

test:
	GOCACHE=$(GOCACHE) go test $(GO_TEST_FLAGS) $(TEST_PACKAGES)

smoketest: build
	$(PYTHON) tools/pydantic_ai_test_mcp.py $(SMOKETEST_MODEL)

modeltest:
	./test-models.sh -p "$(MODELTEST_PROMPT)"

clean:
	rm -f $(BINARY_NAME) $(CLI_NAME)
	rm -f *.log
	rm -f investigation-*.md investigation-export-*.md

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
