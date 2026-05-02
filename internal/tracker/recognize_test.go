package tracker

import (
	"net/url"
	"strings"
	"testing"
)

// twentyByte returns a 20-byte string for use as peer_id or v1 info_hash.
func twentyByte(seed byte) string {
	b := make([]byte, 20)
	for i := range b {
		b[i] = seed
	}
	return string(b)
}

// thirtyTwoByte returns a 32-byte string for use as a v2 info_hash.
func thirtyTwoByte(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed
	}
	return string(b)
}

// validQuery builds a minimally valid announce query and applies overrides.
// Pass key="<delete>" to remove a key, or key="<value>" to set/replace it.
func validQuery(overrides ...[2]string) url.Values {
	q := url.Values{}
	q.Set("info_hash", twentyByte('h'))
	q.Set("peer_id", twentyByte('p'))
	q.Set("port", "6881")
	for _, o := range overrides {
		if o[1] == "<delete>" {
			q.Del(o[0])
		} else {
			q.Set(o[0], o[1])
		}
	}
	return q
}

func TestRecognize_HappyPath(t *testing.T) {
	q := validQuery(
		[2]string{"uploaded", "100"},
		[2]string{"downloaded", "200"},
		[2]string{"left", "300"},
		[2]string{"event", "started"},
		[2]string{"numwant", "50"},
		[2]string{"compact", "1"},
	)
	p, err := RecognizeAnnounce(q)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if p.InfoHashLen != 20 {
		t.Errorf("InfoHashLen=%d, want 20", p.InfoHashLen)
	}
	if p.Port != 6881 {
		t.Errorf("Port=%d, want 6881", p.Port)
	}
	if p.Uploaded != 100 || p.Downloaded != 200 || p.Left != 300 {
		t.Errorf("stats=%d/%d/%d, want 100/200/300", p.Uploaded, p.Downloaded, p.Left)
	}
	if p.Event != EventStarted {
		t.Errorf("Event=%d, want EventStarted", p.Event)
	}
	if p.NumWant != 50 {
		t.Errorf("NumWant=%d, want 50", p.NumWant)
	}
	if !p.Compact {
		t.Error("Compact=false, want true")
	}
}

func TestRecognize_V2Hash(t *testing.T) {
	q := validQuery([2]string{"info_hash", thirtyTwoByte('v')})
	p, err := RecognizeAnnounce(q)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if p.InfoHashLen != 32 {
		t.Errorf("InfoHashLen=%d, want 32", p.InfoHashLen)
	}
}

func TestRecognize_Defaults(t *testing.T) {
	// Minimal valid request — all optional fields absent
	q := validQuery()
	p, err := RecognizeAnnounce(q)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if p.Uploaded != 0 || p.Downloaded != 0 || p.Left != 0 {
		t.Errorf("expected stats default to 0, got %d/%d/%d", p.Uploaded, p.Downloaded, p.Left)
	}
	if p.Event != EventNone {
		t.Errorf("expected EventNone, got %d", p.Event)
	}
	if p.NumWant != -1 {
		t.Errorf("expected NumWant=-1, got %d", p.NumWant)
	}
	if !p.Compact {
		t.Error("expected Compact=true by default")
	}
}

func TestRecognize_Rejections(t *testing.T) {
	cases := []struct {
		name      string
		overrides [][2]string
		wantSub   string
	}{
		// Required field absence
		{"missing info_hash", [][2]string{{"info_hash", "<delete>"}}, "info_hash"},
		{"missing peer_id", [][2]string{{"peer_id", "<delete>"}}, "peer_id"},
		{"missing port", [][2]string{{"port", "<delete>"}}, "port"},

		// info_hash bounds
		{"info_hash 19", [][2]string{{"info_hash", strings.Repeat("a", 19)}}, "20 or 32"},
		{"info_hash 21", [][2]string{{"info_hash", strings.Repeat("a", 21)}}, "20 or 32"},
		{"info_hash 31", [][2]string{{"info_hash", strings.Repeat("a", 31)}}, "20 or 32"},
		{"info_hash 33", [][2]string{{"info_hash", strings.Repeat("a", 33)}}, "20 or 32"},

		// peer_id strictness
		{"peer_id 19", [][2]string{{"peer_id", strings.Repeat("a", 19)}}, "exactly 20"},
		{"peer_id 21", [][2]string{{"peer_id", strings.Repeat("a", 21)}}, "exactly 20"},

		// Port bounds
		{"port 0", [][2]string{{"port", "0"}}, "port"},
		{"port negative", [][2]string{{"port", "-1"}}, "port"},
		{"port 65536", [][2]string{{"port", "65536"}}, "port"},
		{"port abc", [][2]string{{"port", "abc"}}, "port"},

		// Stats negative/garbage
		{"uploaded negative", [][2]string{{"uploaded", "-1"}}, "uploaded"},
		{"uploaded garbage", [][2]string{{"uploaded", "abc"}}, "uploaded"},
		{"downloaded negative", [][2]string{{"downloaded", "-5"}}, "downloaded"},
		{"downloaded garbage", [][2]string{{"downloaded", "x"}}, "downloaded"},
		{"left negative", [][2]string{{"left", "-1"}}, "left"},
		{"left garbage", [][2]string{{"left", "junk"}}, "left"},

		// Event enum
		{"event uppercase", [][2]string{{"event", "STARTED"}}, "event"},
		{"event garbage", [][2]string{{"event", "garbage"}}, "event"},
		{"event paused", [][2]string{{"event", "paused"}}, "event"},

		// numwant bounds
		{"numwant -1", [][2]string{{"numwant", "-1"}}, "numwant"},
		{"numwant too big", [][2]string{{"numwant", "1001"}}, "numwant"},
		{"numwant garbage", [][2]string{{"numwant", "abc"}}, "numwant"},

		// Compact
		{"compact 2", [][2]string{{"compact", "2"}}, "compact"},
		{"compact yes", [][2]string{{"compact", "yes"}}, "compact"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := validQuery(c.overrides...)
			_, err := RecognizeAnnounce(q)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantSub)
			}
		})
	}
}

func TestRecognize_AcceptsBoundaryValues(t *testing.T) {
	cases := []struct {
		name      string
		overrides [][2]string
	}{
		{"port 1", [][2]string{{"port", "1"}}},
		{"port 65535", [][2]string{{"port", "65535"}}},
		{"numwant 0", [][2]string{{"numwant", "0"}}},
		{"numwant max", [][2]string{{"numwant", "1000"}}},
		{"uploaded 0", [][2]string{{"uploaded", "0"}}},
		{"compact 0", [][2]string{{"compact", "0"}}},
		{"event stopped", [][2]string{{"event", "stopped"}}},
		{"event completed", [][2]string{{"event", "completed"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := validQuery(c.overrides...)
			if _, err := RecognizeAnnounce(q); err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

func TestRecognize_EventMapping(t *testing.T) {
	cases := map[string]PeerEvent{
		"":          EventNone,
		"started":   EventStarted,
		"stopped":   EventStopped,
		"completed": EventCompleted,
	}
	for raw, want := range cases {
		t.Run("event="+raw, func(t *testing.T) {
			var overrides [][2]string
			if raw == "" {
				overrides = [][2]string{{"event", "<delete>"}}
			} else {
				overrides = [][2]string{{"event", raw}}
			}
			p, err := RecognizeAnnounce(validQuery(overrides...))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Event != want {
				t.Errorf("got %d, want %d", p.Event, want)
			}
		})
	}
}

func TestRecognize_InfoHashHex(t *testing.T) {
	// 20 bytes of 0xab → hex string of 40 'a''b' pairs
	q := validQuery([2]string{"info_hash", string(make([]byte, 20))})
	q.Set("info_hash", strings.Repeat("\xab", 20))
	p, err := RecognizeAnnounce(q)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	hex := p.InfoHashHex()
	want := strings.Repeat("ab", 20)
	if hex != want {
		t.Errorf("InfoHashHex=%q, want %q", hex, want)
	}
}

func TestRecognize_PeerIDString(t *testing.T) {
	id := twentyByte('q')
	q := validQuery([2]string{"peer_id", id})
	p, err := RecognizeAnnounce(q)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if p.PeerIDString() != id {
		t.Errorf("PeerIDString=%q, want %q", p.PeerIDString(), id)
	}
}
