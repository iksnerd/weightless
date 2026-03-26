package tracker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

var testSecret string

// SetTrackerSecret updates the internal secret for testing purposes.
func SetTrackerSecret(s string) {
	testSecret = s
}

func getSecret() string {
	if testSecret != "" {
		return testSecret
	}
	return os.Getenv("TRACKER_SECRET")
}

// VerifyPasskey checks if a passkey is a valid signed user_id.
// Format: "user_id.signature"
func VerifyPasskey(passkey string) (string, error) {
	secret := getSecret()
	if secret == "" {
		return "", fmt.Errorf("TRACKER_SECRET not set on server")
	}

	parts := strings.Split(passkey, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid passkey format")
	}

	userID := parts[0]
	signature := parts[1]

	expectedSig := SignUserID(userID)
	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return "", fmt.Errorf("invalid signature")
	}

	return userID, nil
}

// SignUserID generates a signature for a userID using the server secret.
func SignUserID(userID string) string {
	secret := getSecret()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(userID))
	return hex.EncodeToString(mac.Sum(nil))
}
