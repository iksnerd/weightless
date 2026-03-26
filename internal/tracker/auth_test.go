package tracker

import (
	"os"
	"testing"
)

func TestAuthLogic(t *testing.T) {
	// Use helper to set secret
	oldSecret := os.Getenv("TRACKER_SECRET")
	SetTrackerSecret("super-secret-key")
	defer SetTrackerSecret(oldSecret)

	userID := "user_clerk_123"

	// 1. Test Signing
	signature := SignUserID(userID)
	if signature == "" {
		t.Fatal("Signature should not be empty")
	}

	// 2. Test Verification Success
	passkey := userID + "." + signature
	verifiedID, err := VerifyPasskey(passkey)
	if err != nil {
		t.Fatalf("Verification failed: %v", err)
	}
	if verifiedID != userID {
		t.Errorf("Expected userID %s, got %s", userID, verifiedID)
	}

	// 3. Test Verification Failure (Wrong Signature)
	badPasskey := userID + ".wrongsignature"
	_, err = VerifyPasskey(badPasskey)
	if err == nil {
		t.Error("Expected error for bad signature, got nil")
	}

	// 4. Test Verification Failure (Malformed Format)
	_, err = VerifyPasskey("no-dot-format")
	if err == nil {
		t.Error("Expected error for malformed passkey, got nil")
	}

	// 5. Test Verification Failure (Empty Secret)
	SetTrackerSecret("")
	_, err = VerifyPasskey(passkey)
	if err == nil {
		t.Error("Expected error for empty TRACKER_SECRET, got nil")
	}
}
