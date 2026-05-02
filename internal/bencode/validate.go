// Package bencode provides a LangSec-style structural validator for bencoded
// data (BEP 3). Inspired by Sassaman & Patterson, "The Science of Insecurity":
// recognize completely against a bounded grammar before any execution
// (decoding, indexing, recursion) touches the input.
//
// Validate walks the bytes without allocating the decoded tree. It enforces
// configurable limits on nesting depth, individual string length, list/dict
// element counts, and total input size. A successful Validate guarantees the
// input is well-formed bencode within those bounds — only then is it safe to
// hand to a permissive decoder like github.com/zeebo/bencode.
package bencode

import (
	"errors"
	"fmt"
)

// Limits bounds the structural complexity of a bencoded payload.
// Callers should pick limits sized for their threat model: a .torrent file
// from an untrusted source can be large and deeply nested; a peer-protocol
// extension handshake should be small and shallow.
type Limits struct {
	MaxBytes       int // total input size; 0 means no overall byte cap
	MaxDepth       int // nesting depth of lists+dicts; 0 forbids any container
	MaxStrLen      int // max length of any single byte string
	MaxListLen     int // max elements per list
	MaxKeysPerDict int // max keys per dict
}

// TorrentLimits is sized for hybrid v1+v2 .torrent files. The pieces string
// and v2 piece-layer entries can be megabytes for large content. File trees
// (BEP 52) rarely exceed depth ~8 in practice, so 64 is generous DoS bound.
var TorrentLimits = Limits{
	MaxBytes:       16 << 20, // 16 MiB
	MaxDepth:       64,
	MaxStrLen:      8 << 20, // 8 MiB — covers very long pieces strings
	MaxListLen:     1 << 17, // 131072 — files in a huge multi-file torrent
	MaxKeysPerDict: 1 << 14, // 16384 — file tree directory fan-out
}

// PeerMessageLimits is sized for BEP 10 extension handshakes and BEP 9
// metadata-piece headers. These travel on a peer connection and should be
// small; a few-kilobyte cap kills oversized-allocation attempts at the door.
var PeerMessageLimits = Limits{
	MaxBytes:       1 << 20, // 1 MiB — extension handshakes can carry feature dicts
	MaxDepth:       16,
	MaxStrLen:      256 << 10, // 256 KiB
	MaxListLen:     1024,
	MaxKeysPerDict: 256,
}

// TrackerResponseLimits is sized for BEP 3 tracker responses. The peers list
// (compact or dictionary form) is the dominant size, capped here at ~1 MiB.
var TrackerResponseLimits = Limits{
	MaxBytes:       2 << 20, // 2 MiB
	MaxDepth:       16,
	MaxStrLen:      1 << 20, // 1 MiB — compact peers string for huge swarms
	MaxListLen:     65536,
	MaxKeysPerDict: 256,
}

// Validation errors. Callers can use errors.Is to distinguish them, but the
// wrapped error message contains the offset and is enough for diagnostics.
var (
	ErrTooLarge       = errors.New("bencode: input exceeds MaxBytes")
	ErrEOF            = errors.New("bencode: unexpected end of input")
	ErrTrailingData   = errors.New("bencode: trailing data after value")
	ErrSyntax         = errors.New("bencode: malformed token")
	ErrDepth          = errors.New("bencode: nesting exceeds MaxDepth")
	ErrStringTooLong  = errors.New("bencode: string exceeds MaxStrLen")
	ErrListTooLong    = errors.New("bencode: list exceeds MaxListLen")
	ErrDictTooLarge   = errors.New("bencode: dict exceeds MaxKeysPerDict")
	ErrIntegerSyntax  = errors.New("bencode: malformed integer")
	ErrNegativeLength = errors.New("bencode: negative string length")
)

// Validate parses data as bencode and rejects it if any structural rule
// or limit is violated. It does not allocate the decoded value.
func Validate(data []byte, lim Limits) error {
	if lim.MaxBytes > 0 && len(data) > lim.MaxBytes {
		return fmt.Errorf("%w: got %d", ErrTooLarge, len(data))
	}
	p := &parser{data: data, lim: lim}
	if err := p.value(0); err != nil {
		return err
	}
	if p.pos != len(data) {
		return fmt.Errorf("%w: %d trailing byte(s)", ErrTrailingData, len(data)-p.pos)
	}
	return nil
}

type parser struct {
	data []byte
	pos  int
	lim  Limits
}

func (p *parser) value(depth int) error {
	if p.pos >= len(p.data) {
		return ErrEOF
	}
	b := p.data[p.pos]
	switch {
	case b == 'i':
		return p.integer()
	case b == 'l':
		return p.list(depth)
	case b == 'd':
		return p.dict(depth)
	case b >= '0' && b <= '9':
		_, err := p.byteString()
		return err
	default:
		return fmt.Errorf("%w: unexpected %q at offset %d", ErrSyntax, b, p.pos)
	}
}

// integer parses i<digits>e. Bencode forbids leading zeros except for "0"
// and forbids "-0". We enforce both since they signal a malformed encoder.
func (p *parser) integer() error {
	start := p.pos
	p.pos++ // consume 'i'
	if p.pos >= len(p.data) {
		return ErrEOF
	}
	digitStart := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
		if p.pos >= len(p.data) {
			return ErrEOF
		}
	}
	if p.pos >= len(p.data) || p.data[p.pos] < '0' || p.data[p.pos] > '9' {
		return fmt.Errorf("%w at offset %d", ErrIntegerSyntax, start)
	}
	first := p.pos
	for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
		p.pos++
	}
	digits := p.data[first:p.pos]
	// Disallow leading zeros: "i0e" ok, "i00e" / "i01e" / "i-0e" not.
	if len(digits) > 1 && digits[0] == '0' {
		return fmt.Errorf("%w: leading zero at offset %d", ErrIntegerSyntax, start)
	}
	if digitStart != first && digits[0] == '0' {
		// negative zero: "-0"
		return fmt.Errorf("%w: negative zero at offset %d", ErrIntegerSyntax, start)
	}
	if p.pos >= len(p.data) || p.data[p.pos] != 'e' {
		return fmt.Errorf("%w: missing 'e' at offset %d", ErrIntegerSyntax, p.pos)
	}
	p.pos++ // consume 'e'
	return nil
}

// byteString parses <length>:<bytes> and returns the byte content.
func (p *parser) byteString() ([]byte, error) {
	start := p.pos
	// Parse length digits — bencode does NOT allow leading zeros for length,
	// except length "0" itself.
	for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
		p.pos++
	}
	if p.pos == start {
		return nil, fmt.Errorf("%w: expected digit at offset %d", ErrSyntax, start)
	}
	digits := p.data[start:p.pos]
	if len(digits) > 1 && digits[0] == '0' {
		return nil, fmt.Errorf("%w: leading zero in string length at offset %d", ErrSyntax, start)
	}
	// Parse length integer; cap it to int range. We bound by MaxStrLen so
	// even a multi-gig declared length is rejected before we look at memory.
	var length int
	for _, d := range digits {
		// Overflow guard — the MaxStrLen check below catches plausible values.
		if length > (1<<31-1)/10 {
			return nil, fmt.Errorf("%w: length overflow at offset %d", ErrStringTooLong, start)
		}
		length = length*10 + int(d-'0')
	}
	if length < 0 {
		return nil, ErrNegativeLength
	}
	if p.lim.MaxStrLen > 0 && length > p.lim.MaxStrLen {
		return nil, fmt.Errorf("%w: declared %d at offset %d", ErrStringTooLong, length, start)
	}
	if p.pos >= len(p.data) || p.data[p.pos] != ':' {
		return nil, fmt.Errorf("%w: missing ':' at offset %d", ErrSyntax, p.pos)
	}
	p.pos++ // consume ':'
	if p.pos+length > len(p.data) {
		return nil, fmt.Errorf("%w: declared length %d exceeds remaining %d", ErrEOF, length, len(p.data)-p.pos)
	}
	out := p.data[p.pos : p.pos+length]
	p.pos += length
	return out, nil
}

func (p *parser) list(depth int) error {
	if depth+1 > p.lim.MaxDepth {
		return fmt.Errorf("%w: list at offset %d", ErrDepth, p.pos)
	}
	p.pos++ // consume 'l'
	count := 0
	for p.pos < len(p.data) && p.data[p.pos] != 'e' {
		if p.lim.MaxListLen > 0 && count >= p.lim.MaxListLen {
			return fmt.Errorf("%w: at offset %d", ErrListTooLong, p.pos)
		}
		if err := p.value(depth + 1); err != nil {
			return err
		}
		count++
	}
	if p.pos >= len(p.data) {
		return fmt.Errorf("%w: unterminated list", ErrEOF)
	}
	p.pos++ // consume 'e'
	return nil
}

func (p *parser) dict(depth int) error {
	if depth+1 > p.lim.MaxDepth {
		return fmt.Errorf("%w: dict at offset %d", ErrDepth, p.pos)
	}
	p.pos++ // consume 'd'
	keys := 0
	for p.pos < len(p.data) && p.data[p.pos] != 'e' {
		if p.lim.MaxKeysPerDict > 0 && keys >= p.lim.MaxKeysPerDict {
			return fmt.Errorf("%w: at offset %d", ErrDictTooLarge, p.pos)
		}
		// Key must be a byte string.
		if !(p.data[p.pos] >= '0' && p.data[p.pos] <= '9') {
			return fmt.Errorf("%w: dict key at offset %d not a string", ErrSyntax, p.pos)
		}
		if _, err := p.byteString(); err != nil {
			return err
		}
		// Value
		if err := p.value(depth + 1); err != nil {
			return err
		}
		keys++
	}
	if p.pos >= len(p.data) {
		return fmt.Errorf("%w: unterminated dict", ErrEOF)
	}
	p.pos++ // consume 'e'
	return nil
}
