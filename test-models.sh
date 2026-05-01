#!/bin/bash

# Configuration
PROMPT="list indices"

while getopts "p:" opt; do
  case $opt in
    p) PROMPT="$OPTARG" ;;
    \?) echo "Usage: $0 [-p prompt]" >&2; exit 1 ;;
  esac
done

SERVER_BIN="./elastic-mcp-server"
CLI_BIN="./elastic-cli"

# Ensure binaries are built
echo "Building binaries..."
go build -o $SERVER_BIN ./cmd/server/main.go
go build -o $CLI_BIN ./cmd/cli/main.go

export ELASTIC_MCP_SERVER=$SERVER_BIN
export CLIENT_LOG_LEVEL=debug
export CLIENT_LOG_PAYLOADS=true
export SERVER_LOG_LEVEL=debug

test_model() {
    local model=$1
    local timestamp=$(date +%Y%m%d_%H%M%S)
    local client_logfile="test_client_${model}_${timestamp}.log"
    local server_logfile="test_server_${model}_${timestamp}.log"
    
    echo "--------------------------------------------------"
    echo "Testing model: $model"
    echo "Prompt: $PROMPT"
    echo "Client Log: $client_logfile"
    echo "Server Log: $server_logfile"
    echo "--------------------------------------------------"
    
    CLIENT_LOG_FILE="$client_logfile" SERVER_LOG_FILE="$server_logfile" $CLI_BIN --model "$model" --memory=false --prompt "$PROMPT"
    echo -e "\n"
}

# Test models referenced in cmd/cli/main.go
if [ -n "$OPENAI_API_KEY" ]; then
    test_model "gpt-5"
    test_model "gpt-5-mini"
    test_model "gpt-5-nano"
fi

if [ -n "$ANTHROPIC_API_KEY" ]; then
    test_model "claude-sonnet-4-6"
    test_model "claude-haiku-4-5"
    test_model "claude-opus-4-6"
fi

if [ -n "$GEMINI_API_KEY" ]; then
    test_model "gemini-3-flash-preview"
    test_model "gemini-3.1-pro-preview"
fi

if [ -z "$OPENAI_API_KEY" ] && [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$GEMINI_API_KEY" ]; then
    echo "Error: No API keys found (OPENAI_API_KEY, ANTHROPIC_API_KEY, or GEMINI_API_KEY)."
    exit 1
fi
