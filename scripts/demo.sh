#!/usr/bin/env bash
set -euo pipefail
echo "Demo: build"
go build -o orchestrator ./...
echo "Demo: bring up orchestrator"
./orchestrator up
echo "Status:"
./orchestrator status
echo "Waiting 15s to show containers started..."
sleep 15
echo "Down:"
./orchestrator down
