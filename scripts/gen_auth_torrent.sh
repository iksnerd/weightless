#!/bin/bash
set -e

# Configuration
SECRET="${TRACKER_SECRET:-changeme}"
USER_ID="${USER_ID:-user_dev_123}"
TRACKER_BASE="http://localhost:8080"
DATA_FILE="auth-test-data.bin"

echo "--- 1. Generating Signed Passkey ---"
cat << EOF > gen_passkey_tmp.go
package main
import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)
func main() {
	h := hmac.New(sha256.New, []byte("${SECRET}"))
	h.Write([]byte("${USER_ID}"))
	fmt.Printf("%s.%s", "${USER_ID}", hex.EncodeToString(h.Sum(nil)))
}
EOF
PASSKEY=$(go run gen_passkey_tmp.go)
rm gen_passkey_tmp.go

echo "Passkey: $PASSKEY"

echo -e "\n--- 2. Building Tools ---"
./scripts/build.sh

echo -e "\n--- 3. Creating Test Data ---"
if [ ! -f "$DATA_FILE" ]; then
    dd if=/dev/urandom of="$DATA_FILE" bs=1M count=10
    echo "Created 10MB test file: $DATA_FILE"
else
    echo "Using existing test file: $DATA_FILE"
fi

echo -e "\n--- 4. Creating & Registering Torrent ---"
./wl create \
  --weightless "$TRACKER_BASE/announce/$PASSKEY" \
  --publisher "Weightless-Dev" \
  --description "High-performance dataset for Weightless authentication testing." \
  --comment "Automated test swarm" \
  "$DATA_FILE"

echo -e "\n--- Done! ---"
echo "1. Open ${DATA_FILE}.torrent in Transmission."
echo "2. Point download location to this directory."
echo "3. Verify tracker status: curl -s $TRACKER_BASE/metrics | grep tracker_active_peers"
