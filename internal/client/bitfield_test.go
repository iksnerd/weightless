package client

import "testing"

func TestBitfield(t *testing.T) {
	t.Parallel()
	bf := NewBitfield(10)
	if len(bf) != 2 {
		t.Errorf("expected length 2, got %d", len(bf))
	}

	if bf.HasPiece(3) {
		t.Error("expected piece 3 to be missing")
	}

	bf.SetPiece(3)
	if !bf.HasPiece(3) {
		t.Error("expected piece 3 to be present")
	}

	bf.SetPiece(9)
	if !bf.HasPiece(9) {
		t.Error("expected piece 9 to be present")
	}

	if bf.HasPiece(0) {
		t.Error("expected piece 0 to be missing")
	}
}
