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
	signature := SignUserID(userID)
	validPasskey := userID + "." + signature

	tests := []struct {
		name    string
		passkey string
		secret  string
		wantID  string
		wantErr bool
	}{
		{
			name:    "valid passkey",
			passkey: validPasskey,
			secret:  "super-secret-key",
			wantID:  userID,
		},
		{
			name:    "wrong signature",
			passkey: userID + ".wrongsignature",
			secret:  "super-secret-key",
			wantErr: true,
		},
		{
			name:    "no dot separator",
			passkey: "no-dot-format",
			secret:  "super-secret-key",
			wantErr: true,
		},
		{
			name:    "extra segments",
			passkey: "user.sig.extra",
			secret:  "super-secret-key",
			wantErr: true,
		},
		{
			name:    "empty user portion",
			passkey: "." + signature,
			secret:  "super-secret-key",
			wantErr: true,
		},
		{
			name:    "empty signature portion",
			passkey: userID + ".",
			secret:  "super-secret-key",
			wantErr: true,
		},
		{
			name:    "empty passkey",
			passkey: "",
			secret:  "super-secret-key",
			wantErr: true,
		},
		{
			name:    "empty secret",
			passkey: validPasskey,
			secret:  "",
			wantErr: true,
		},
		{
			name:    "unicode user ID",
			passkey: "用户.fakesig",
			secret:  "super-secret-key",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetTrackerSecret(tt.secret)
			gotID, err := VerifyPasskey(tt.passkey)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil (id=%q)", gotID)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotID != tt.wantID {
				t.Errorf("got id %q, want %q", gotID, tt.wantID)
			}
		})
	}
}
