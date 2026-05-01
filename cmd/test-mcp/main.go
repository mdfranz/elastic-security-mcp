package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx := context.Background()

	serverPath := os.Getenv("ELASTIC_MCP_SERVER")
	if serverPath == "" {
		serverPath = "./elastic-mcp-server"
	}

	cmd := exec.Command(serverPath)
	transport := &mcp.CommandTransport{Command: cmd}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer session.Close()

	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		log.Fatalf("failed to list tools: %v", err)
	}

	out, err := json.MarshalIndent(toolsResult, "", "  ")
	if err != nil {
		log.Fatalf("failed to encode tool list: %v", err)
	}
	fmt.Println(string(out))
}
