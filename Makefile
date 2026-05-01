BINARY_NAME=elastic-mcp-server

.PHONY: all build clean run

all: build

build:
	go build -o $(BINARY_NAME) main.go

clean:
	rm -f $(BINARY_NAME)

run: build
	./$(BINARY_NAME)
