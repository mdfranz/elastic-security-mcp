#!/bin/bash
SERVER_BIN="./elastic-mcp-server"

# Function to send a request and wait for a response
send_request() {
    local request=$1
    echo "Sending: $request" >&2
    echo "$request"
}

{
  send_request '{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0.0"}},"id":1}'
  sleep 1
  send_request '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"list_indices","arguments":{}},"id":2}'
  sleep 1
  send_request '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"list_indices","arguments":{}},"id":3}'
} | $SERVER_BIN
