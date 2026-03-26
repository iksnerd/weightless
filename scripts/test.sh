#!/bin/bash
set -e

echo "Running tests with coverage..."
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out

echo "Tests complete."
