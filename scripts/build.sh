#!/bin/bash
set -e

echo "Building Weightless Tracker..."
go build -o weightless ./cmd/tracker/

echo "Building Weightless CLI (wl)..."
go build -o wl ./cmd/wl/

echo "Build complete."
