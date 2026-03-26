package client

// Bitfield represents the pieces available from a peer.
type Bitfield []byte

// HasPiece returns true if the piece at index is present in the bitfield.
func (b Bitfield) HasPiece(index int) bool {
	byteIndex := index / 8
	offset := index % 8
	if byteIndex < 0 || byteIndex >= len(b) {
		return false
	}
	return (b[byteIndex] >> (7 - offset) & 1) != 0
}

// SetPiece sets the bit for the piece at index.
func (b Bitfield) SetPiece(index int) {
	byteIndex := index / 8
	offset := index % 8
	if byteIndex < 0 || byteIndex >= len(b) {
		return
	}
	b[byteIndex] |= 1 << (7 - offset)
}

// NewBitfield creates a bitfield for n pieces.
func NewBitfield(n int) Bitfield {
	return make(Bitfield, (n+7)/8)
}
