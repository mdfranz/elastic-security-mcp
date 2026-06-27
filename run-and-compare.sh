#!/bin/bash

# run-and-compare.sh
# A script to run test-models.sh across multiple branches and organize results for easy comparison.

set -e

BRANCHES=("main" "any-llm-go" "pi-llm-port" "zendev-goai")
RESULT_DIR="test_results"

# Store current branch
STARTING_BRANCH=$(git rev-parse --abbrev-ref HEAD)

# Check for uncommitted changes
if ! git diff-index --quiet HEAD --; then
    echo "Error: You have uncommitted changes on branch '$STARTING_BRANCH'."
    echo "Please commit, stash, or discard them before running this script."
    exit 1
fi

# Ensure cleanup on exit to restore the starting branch
cleanup() {
    echo "Restoring starting branch '$STARTING_BRANCH'..."
    git checkout "$STARTING_BRANCH" &>/dev/null
}
trap cleanup EXIT

# Parse arguments to pass to test-models.sh
PROMPT_ARG=""
while getopts "p:" opt; do
  case $opt in
    p) PROMPT_ARG="-p $OPTARG" ;;
    *) echo "Usage: $0 [-p prompt]" >&2; exit 1 ;;
  esac
done

echo "Starting tests across branches: ${BRANCHES[*]}"
echo "Results will be saved in '$RESULT_DIR'"
echo "--------------------------------------------------"

# Re-create or clean the results directory
mkdir -p "$RESULT_DIR"

for branch in "${BRANCHES[@]}"; do
    echo "=================================================="
    echo "Processing Branch: $branch"
    echo "=================================================="
    
    # Checkout branch
    git checkout "$branch"
    
    # Ensure test-models.sh is executable
    chmod +x test-models.sh
    
    # Run test-models.sh
    # We run it and let it generate timestamped logs in the root directory.
    ./test-models.sh $PROMPT_ARG
    
    # Create target directory for this branch
    BRANCH_OUT_DIR="$RESULT_DIR/$branch"
    mkdir -p "$BRANCH_OUT_DIR"
    
    # Move and rename the generated log files to remove the timestamps for easier diffing
    # The files generated look like: test_client_${model}_${timestamp}.log
    for logfile in test_client_*.log; do
        if [ -f "$logfile" ]; then
            # Extract model name by removing test_client_ prefix and _timestamp.log suffix
            model=$(echo "$logfile" | sed -E 's/^test_client_//; s/_[0-9]{8}_[0-9]{6}\.log$//')
            mv "$logfile" "$BRANCH_OUT_DIR/client_${model}.log"
        fi
    done
    
    for logfile in test_server_*.log; do
        if [ -f "$logfile" ]; then
            # Extract model name by removing test_server_ prefix and _timestamp.log suffix
            model=$(echo "$logfile" | sed -E 's/^test_server_//; s/_[0-9]{8}_[0-9]{6}\.log$//')
            mv "$logfile" "$BRANCH_OUT_DIR/server_${model}.log"
        fi
    done
    
    echo "Completed branch: $branch"
done

echo "--------------------------------------------------"
echo "All tests complete!"
echo "Comparing outputs:"
echo "Logs are organized in '$RESULT_DIR/<branch>/client_<model>.log'"
echo "Example to compare gemini-3.5-flash between main and zendev-goai:"
echo "  diff -u $RESULT_DIR/main/client_gemini-3.5-flash.log $RESULT_DIR/zendev-goai/client_gemini-3.5-flash.log"
echo "--------------------------------------------------"
