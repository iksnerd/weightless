#!/bin/sh
set -e

# Restore the database from Litestream if it doesn't exist
if [ ! -f /data/weightless.db ]; then
    echo "Restoring database from replica..."
    # If restore fails (e.g. first run), just continue and let initSchema create it
    litestream restore -if-db-not-exists -if-replica-exists /data/weightless.db || echo "No replica found, starting fresh."
fi

# Run litestream replicate in the background
echo "Starting Litestream replication..."
litestream replicate -config /etc/litestream.yml &
LITESTREAM_PID=$!

# Start the weightless application in the background
echo "Starting Weightless Tracker..."
weightless &
TRACKER_PID=$!

# Define shutdown handler
_term() {
  echo "Caught SIGTERM, shutting down gracefully..."
  kill -TERM "$TRACKER_PID" 2>/dev/null
  wait "$TRACKER_PID"
  echo "Tracker stopped. Finalizing Litestream replication..."
  kill -TERM "$LITESTREAM_PID" 2>/dev/null
  wait "$LITESTREAM_PID"
  echo "Shutdown complete."
  exit 0
}

# Trap signals
trap _term SIGTERM SIGINT

# Wait for weightless to exit (or for trap to catch signal)
wait "$TRACKER_PID"
