package client

import (
	"net"
	"testing"

	"github.com/zeebo/bencode"
)

// encodePexDict bencodes a ut_pex message dict from raw compact peer bytes.
func encodePexDict(t *testing.T, added, added6 []byte) []byte {
	t.Helper()
	dict := struct {
		Added  []byte `bencode:"added"`
		Added6 []byte `bencode:"added6"`
	}{added, added6}
	data, err := bencode.EncodeBytes(dict)
	if err != nil {
		t.Fatalf("encode pex dict: %v", err)
	}
	return data
}

func TestParsePexPeers(t *testing.T) {
	// Two IPv4 peers: 1.2.3.4:6881, 5.6.7.8:6882 (6 bytes each).
	added := []byte{1, 2, 3, 4, 0x1a, 0xe1, 5, 6, 7, 8, 0x1a, 0xe2}
	// One IPv6 peer: [::1]:6883 (18 bytes).
	v6 := net.ParseIP("::1").To16()
	added6 := append(append([]byte{}, v6...), 0x1a, 0xe3)

	peers, err := parsePexPeers(encodePexDict(t, added, added6))
	if err != nil {
		t.Fatalf("parsePexPeers: %v", err)
	}

	want := []string{"1.2.3.4:6881", "5.6.7.8:6882", "[::1]:6883"}
	if len(peers) != len(want) {
		t.Fatalf("got %d peers %v, want %d %v", len(peers), peers, len(want), want)
	}
	for i := range want {
		if peers[i] != want[i] {
			t.Errorf("peer %d: got %q, want %q", i, peers[i], want[i])
		}
	}
}

func TestParsePexPeersDropsUnconnectable(t *testing.T) {
	// A real client (Transmission, observed in e2e) can advertise a peer with
	// port 0; an unspecified IP is likewise unconnectable. Both must be dropped
	// while the valid peer survives.
	added := []byte{
		1, 2, 3, 4, 0x1a, 0xe1, // 1.2.3.4:6881  (valid)
		127, 0, 0, 1, 0, 0, // 127.0.0.1:0   (port 0 — drop)
		0, 0, 0, 0, 0x1a, 0xe1, // 0.0.0.0:6881  (unspecified — drop)
	}
	peers, err := parsePexPeers(encodePexDict(t, added, nil))
	if err != nil {
		t.Fatalf("parsePexPeers: %v", err)
	}
	if len(peers) != 1 || peers[0] != "1.2.3.4:6881" {
		t.Errorf("expected only [1.2.3.4:6881], got %v", peers)
	}
}

func TestParsePexPeersEmpty(t *testing.T) {
	// A dict with no added/added6 keys is valid and yields no peers.
	peers, err := parsePexPeers(encodePexDict(t, nil, nil))
	if err != nil {
		t.Fatalf("parsePexPeers: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("expected 0 peers, got %v", peers)
	}
}

func TestParsePexPeersRejectsMalformed(t *testing.T) {
	tests := []struct {
		name string
		dict []byte
	}{
		{"not bencode", []byte("definitely not bencode")},
		// added length 5 is not a clean multiple of the 6-byte v4 entry size.
		{"ragged added", encodePexDict(t, []byte{1, 2, 3, 4, 0x1a}, nil)},
		// added6 length 17 is not a clean multiple of the 18-byte v6 entry size.
		{"ragged added6", encodePexDict(t, nil, make([]byte, 17))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parsePexPeers(tt.dict); err == nil {
				t.Errorf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestDrainPexPeers(t *testing.T) {
	added := []byte{1, 2, 3, 4, 0x1a, 0xe1}
	p := &PeerConn{}

	// Malformed PEX must be silently dropped, never appended.
	p.handlePexMessage([]byte("garbage"))
	if got := p.DrainPexPeers(); got != nil {
		t.Fatalf("malformed pex should yield no peers, got %v", got)
	}

	// Two messages accumulate; drain returns all and clears.
	p.handlePexMessage(encodePexDict(t, added, nil))
	p.handlePexMessage(encodePexDict(t, added, nil))
	got := p.DrainPexPeers()
	if len(got) != 2 {
		t.Fatalf("expected 2 buffered peers, got %v", got)
	}
	if got := p.DrainPexPeers(); got != nil {
		t.Errorf("drain should have cleared the buffer, got %v", got)
	}
}

func TestExtendedHandshakeAdvertisesPex(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := &PeerConn{conn: client, state: stateHandshook}
	go func() {
		_ = p.sendExtendedHandshake()
	}()

	m, err := ReadMessage(server)
	if err != nil {
		t.Fatalf("read extended handshake: %v", err)
	}
	if m.ID != MsgExtended {
		t.Fatalf("expected MsgExtended, got %d", m.ID)
	}
	if m.Payload[0] != 0 {
		t.Fatalf("expected handshake ext-id 0, got %d", m.Payload[0])
	}

	var hs struct {
		M map[string]int `bencode:"m"`
	}
	if err := bencode.DecodeBytes(m.Payload[1:], &hs); err != nil {
		t.Fatalf("decode handshake: %v", err)
	}
	if hs.M[ExtMetadata] != localMetadataID {
		t.Errorf("ut_metadata: got %d, want %d", hs.M[ExtMetadata], localMetadataID)
	}
	if hs.M[ExtPex] != localPexID {
		t.Errorf("ut_pex: got %d, want %d", hs.M[ExtPex], localPexID)
	}
}
