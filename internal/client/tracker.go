package client

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/zeebo/bencode"

	wbencode "weightless/internal/bencode"
)

// Announce sends a request to the tracker and returns a list of peer addresses (IP:Port).
func Announce(ctx context.Context, trackerURL, infoHash, peerID string, port int, left int64) ([]string, error) {
	u, err := url.Parse(trackerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid tracker url: %w", err)
	}

	q := u.Query()
	// Must not be URL encoded by url.Values as it's binary data,
	// but url.Values.Encode() will percent-encode it which is correct for BEP 3
	q.Set("info_hash", infoHash)
	q.Set("peer_id", peerID)
	q.Set("port", strconv.Itoa(port))
	q.Set("uploaded", "0")
	q.Set("downloaded", "0")
	q.Set("left", strconv.FormatInt(left, 10))
	q.Set("compact", "1") // We only support compact peer lists
	q.Set("event", "started")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create tracker request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tracker request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read tracker response: %w", err)
	}

	if err := wbencode.Validate(data, wbencode.TrackerResponseLimits); err != nil {
		return nil, fmt.Errorf("validate tracker response: %w", err)
	}

	var trackerResponse struct {
		FailureReason string `bencode:"failure reason"`
		Peers         string `bencode:"peers"`
		Peers6        string `bencode:"peers6"`
	}

	if err := bencode.DecodeBytes(data, &trackerResponse); err != nil {
		return nil, fmt.Errorf("decode tracker response: %w", err)
	}

	if trackerResponse.FailureReason != "" {
		return nil, fmt.Errorf("tracker failure: %s", trackerResponse.FailureReason)
	}

	// BEP 3 compact IPv4 (6 bytes/peer) plus BEP 7 compact IPv6 (18 bytes/peer).
	addrs, err := unpackPeers([]byte(trackerResponse.Peers), net.IPv4len)
	if err != nil {
		return nil, err
	}
	addrs6, err := unpackPeers([]byte(trackerResponse.Peers6), net.IPv6len)
	if err != nil {
		return nil, err
	}
	return append(addrs, addrs6...), nil
}

// unpackPeers parses a compact peer list: ipLen address bytes (4 for IPv4,
// 16 for IPv6) followed by a 2-byte big-endian port, per peer.
func unpackPeers(peers []byte, ipLen int) ([]string, error) {
	peerSize := ipLen + 2
	if len(peers)%peerSize != 0 {
		return nil, fmt.Errorf("invalid peers string length: %d", len(peers))
	}

	numPeers := len(peers) / peerSize
	addrs := make([]string, numPeers)

	for i := 0; i < numPeers; i++ {
		offset := i * peerSize
		ip := net.IP(peers[offset : offset+ipLen])
		port := binary.BigEndian.Uint16(peers[offset+ipLen : offset+peerSize])
		addrs[i] = net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
	}

	return addrs, nil
}
