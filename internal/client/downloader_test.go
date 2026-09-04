package client

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/zeebo/bencode"
)

// A torrent whose piece-hash string doesn't cover every piece must be
// rejected up front: otherwise every piece fails the worker's hash-index
// range check and the collector re-enqueues it forever.
func TestDownloadMVPRejectsBadPieceHashes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pieces  []byte
		wantErr string
	}{
		{"v2-only (no v1 hashes)", nil, "no v1 piece hashes"},
		{"truncated", make([]byte, 20), "length mismatch"},
		{"over-long", make([]byte, 60), "length mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// A tracker that must never be contacted.
			tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("tracker contacted with bad piece hashes: %s", r.URL)
			}))
			defer tracker.Close()

			err := DownloadMVP(context.Background(), DownloadOptions{
				Meta: TorrentMeta{
					Name:        "bad",
					InfoHashV1:  make([]byte, 20),
					PieceLength: 16,
					PieceCount:  2,
					TotalSize:   24,
					Pieces:      tt.pieces,
					Files:       []FileEntry{{Path: "bad.dat", Length: 24}},
				},
				TrackerURL: tracker.URL,
				OutputDir:  t.TempDir(),
			})
			if err == nil {
				t.Fatal("DownloadMVP accepted a torrent with bad piece hashes")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// recordingTracker is an httptest tracker that records every announce's query
// and answers with the given compact peer list.
type recordingTracker struct {
	*httptest.Server
	mu    sync.Mutex
	calls []url.Values
}

func newRecordingTracker(t *testing.T, peerAddrs ...string) *recordingTracker {
	t.Helper()
	var peers []byte
	for _, a := range peerAddrs {
		host, port, err := net.SplitHostPort(a)
		if err != nil {
			t.Fatal(err)
		}
		p, err := net.LookupPort("tcp", port)
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, net.ParseIP(host).To4()...)
		peers = binary.BigEndian.AppendUint16(peers, uint16(p))
	}
	rt := &recordingTracker{}
	rt.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt.mu.Lock()
		rt.calls = append(rt.calls, r.URL.Query())
		rt.mu.Unlock()
		data, _ := bencode.EncodeBytes(map[string]interface{}{"interval": 1800, "peers": string(peers)})
		w.Write(data)
	}))
	t.Cleanup(rt.Close)
	return rt
}

func (rt *recordingTracker) events() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]string, len(rt.calls))
	for i, c := range rt.calls {
		out[i] = c.Get("event")
	}
	return out
}

func (rt *recordingTracker) call(i int) url.Values {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.calls[i]
}

func assertEvents(t *testing.T, rt *recordingTracker, want ...string) {
	t.Helper()
	got := rt.events()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("announce events = %v, want %v", got, want)
	}
}

// A successful download reports completion (so the tracker counts it and
// credits the bytes), then leaves — this client doesn't seed, so it must not
// linger in the swarm as a phantom peer.
func TestDownloadMVPAnnouncesCompletedThenStopped(t *testing.T) {
	t.Parallel()
	piece0 := bytes.Repeat([]byte{'A'}, 16)
	piece1 := bytes.Repeat([]byte{'B'}, 8)
	peerAddr := startTestPeer(t, piece0, piece1)
	meta, _, _, _ := twoPieceFixture(t)

	rt := newRecordingTracker(t, peerAddr)
	err := DownloadMVP(context.Background(), DownloadOptions{
		Meta:       meta,
		TrackerURL: rt.URL,
		OutputDir:  t.TempDir(),
		MaxWorkers: 1,
	})
	if err != nil {
		t.Fatalf("DownloadMVP: %v", err)
	}

	assertEvents(t, rt, EventStarted, EventCompleted, EventStopped)

	started := rt.call(0)
	if started.Get("left") != "24" || started.Get("downloaded") != "0" || started.Get("uploaded") != "0" {
		t.Errorf("started: left=%s downloaded=%s uploaded=%s, want 24/0/0",
			started.Get("left"), started.Get("downloaded"), started.Get("uploaded"))
	}
	completed := rt.call(1)
	if completed.Get("left") != "0" || completed.Get("downloaded") != "24" || completed.Get("uploaded") != "0" {
		t.Errorf("completed: left=%s downloaded=%s uploaded=%s, want 0/24/0",
			completed.Get("left"), completed.Get("downloaded"), completed.Get("uploaded"))
	}
	if completed.Get("peer_id") != started.Get("peer_id") {
		t.Errorf("peer_id changed between announces: %q vs %q", started.Get("peer_id"), completed.Get("peer_id"))
	}
}

// When the tracker returns no peers we have already registered ourselves, so
// we must un-register with a stopped announce rather than silently vanish.
func TestDownloadMVPAnnouncesStoppedWhenNoPeers(t *testing.T) {
	t.Parallel()
	meta, _, _, _ := twoPieceFixture(t)
	rt := newRecordingTracker(t) // zero peers

	err := DownloadMVP(context.Background(), DownloadOptions{
		Meta:       meta,
		TrackerURL: rt.URL,
		OutputDir:  t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "no peers found") {
		t.Fatalf("err = %v, want no peers found", err)
	}
	assertEvents(t, rt, EventStarted, EventStopped)
}

// A swarm failure after the started announce also ends with stopped — even
// when the failure is the caller's ctx being cancelled, because the stopped
// announce runs on its own background context.
func TestDownloadMVPAnnouncesStoppedOnSwarmFailure(t *testing.T) {
	t.Parallel()
	meta, _, _, _ := twoPieceFixture(t)
	rt := newRecordingTracker(t, deadAddr(t)) // only a dead peer: swarm exhausts

	err := DownloadMVP(context.Background(), DownloadOptions{
		Meta:       meta,
		TrackerURL: rt.URL,
		OutputDir:  t.TempDir(),
		MaxWorkers: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "swarm download") {
		t.Fatalf("err = %v, want swarm download failure", err)
	}
	assertEvents(t, rt, EventStarted, EventStopped)
	stopped := rt.call(1)
	if stopped.Get("uploaded") != "0" {
		t.Errorf("stopped uploaded = %s, want 0 (client never seeds)", stopped.Get("uploaded"))
	}
}

func TestDownloadMVPAnnouncesStoppedOnCancel(t *testing.T) {
	t.Parallel()
	meta, _, _, _ := twoPieceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A peer that handshakes, pulls the plug on the download ctx, then hangs
	// up. By the time it's dialled the started announce has already
	// returned, so the download can only end by cancellation — and stopped
	// must still be sent even though ctx is dead.
	stall := startPeer(t, func(conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, 68)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		conn.Write(buf)
		cancel()
	})
	rt := newRecordingTracker(t, stall)

	err := DownloadMVP(ctx, DownloadOptions{
		Meta:       meta,
		TrackerURL: rt.URL,
		OutputDir:  t.TempDir(),
		MaxWorkers: 1,
	})
	if err == nil {
		t.Fatal("expected failure after ctx cancel")
	}
	assertEvents(t, rt, EventStarted, EventStopped)
}

func TestGeneratePeerID(t *testing.T) {
	t.Parallel()
	a, b := GeneratePeerID(), GeneratePeerID()
	if len(a) != 20 || !strings.HasPrefix(a, "-WL0020-") {
		t.Errorf("GeneratePeerID() = %q, want 20 bytes with -WL0020- prefix", a)
	}
	if a == b {
		t.Errorf("two peer ids collided: %q", a)
	}
}
