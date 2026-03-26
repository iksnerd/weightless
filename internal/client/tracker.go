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

	var trackerResponse struct {
		FailureReason string `bencode:"failure reason"`
		Peers         string `bencode:"peers"`
	}

	if err := bencode.DecodeBytes(data, &trackerResponse); err != nil {
		return nil, fmt.Errorf("decode tracker response: %w", err)
	}

	if trackerResponse.FailureReason != "" {
		return nil, fmt.Errorf("tracker failure: %s", trackerResponse.FailureReason)
	}

	return unpackPeers([]byte(trackerResponse.Peers))
}

// unpackPeers parses the compact BEP 3 peer list (6 bytes per peer: 4 for IPv4, 2 for port).
func unpackPeers(peers []byte) ([]string, error) {
	const peerSize = 6
	if len(peers)%peerSize != 0 {
		return nil, fmt.Errorf("invalid peers string length: %d", len(peers))
	}

	numPeers := len(peers) / peerSize
	addrs := make([]string, numPeers)

	for i := 0; i < numPeers; i++ {
		offset := i * peerSize
		ip := net.IPv4(peers[offset], peers[offset+1], peers[offset+2], peers[offset+3])
		port := binary.BigEndian.Uint16(peers[offset+4 : offset+6])
		addrs[i] = net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
	}

	return addrs, nil
}
