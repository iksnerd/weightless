#!/bin/bash
set -e

# Build first
./scripts/build.sh

echo "Starting Weightless Tracker locally..."
export DB_PATH="./weightless.db"
export PORT="8080"
./weightless
