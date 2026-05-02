package bencode

import (
	"errors"
	"strings"
	"testing"
)

// looseLimits accepts almost anything — for tests that only care about
// structural correctness, not size bounds.
var looseLimits = Limits{
	MaxBytes:       1 << 30,
	MaxDepth:       64,
	MaxStrLen:      1 << 24,
	MaxListLen:     1 << 16,
	MaxKeysPerDict: 1 << 14,
}

func TestValidate_Valid(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"empty string", "0:"},
		{"short string", "5:hello"},
		{"zero", "i0e"},
		{"negative", "i-42e"},
		{"large positive", "i9223372036854775807e"},
		{"empty list", "le"},
		{"list of ints", "li1ei2ei3ee"},
		{"empty dict", "de"},
		{"single-key dict", "d3:foo3:bare"},
		{"nested dict", "d4:infod4:name5:hellodes ee"[:21] + "ee"},
		{"multi-key dict", "d1:ai1e1:bi2ee"},
		{"nested list", "lli1eel3:fooee"},
		{"realistic torrent meta", "d8:announce20:http://tracker.test/4:infod6:lengthi100e4:name4:test12:piece lengthi16384e6:pieces20:" + strings.Repeat("\x00", 20) + "ee"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Validate([]byte(c.data), looseLimits); err != nil {
				t.Errorf("expected ok, got %v", err)
			}
		})
	}
}

func TestValidate_Malformed(t *testing.T) {
	cases := []struct {
		name string
		data string
		want error
	}{
		{"empty input", "", ErrEOF},
		{"trailing data", "i1ee", ErrTrailingData},
		{"int missing e", "i42", ErrIntegerSyntax},
		{"int empty", "ie", ErrIntegerSyntax},
		{"int leading zero", "i01e", ErrIntegerSyntax},
		{"int negative zero", "i-0e", ErrIntegerSyntax},
		{"int just minus", "i-e", ErrIntegerSyntax},
		{"string missing colon", "5hello", ErrSyntax},
		{"string declared too long", "10:hi", ErrEOF},
		{"string leading-zero length", "01:x", ErrSyntax},
		{"list unterminated", "li1e", ErrEOF},
		{"dict unterminated", "d3:fooi1e", ErrEOF},
		{"dict non-string key", "di1ei1ee", ErrSyntax},
		{"unknown leading byte", "x", ErrSyntax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate([]byte(c.data), looseLimits)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, c.want) {
				t.Errorf("got %v, want errors.Is %v", err, c.want)
			}
		})
	}
}

func TestValidate_DepthLimit(t *testing.T) {
	// Build l<l<l<...le...>e>e — depth N
	build := func(n int) string {
		return strings.Repeat("l", n) + strings.Repeat("e", n)
	}
	lim := looseLimits
	lim.MaxDepth = 3

	if err := Validate([]byte(build(3)), lim); err != nil {
		t.Errorf("depth 3 with MaxDepth=3 should pass, got %v", err)
	}
	err := Validate([]byte(build(4)), lim)
	if err == nil {
		t.Fatal("depth 4 with MaxDepth=3 should fail")
	}
	if !errors.Is(err, ErrDepth) {
		t.Errorf("expected ErrDepth, got %v", err)
	}

	// Mixed dict+list nesting also counts.
	mixed := "d1:al1:al1:al1:aleeeee" // d > l > l > l (depth 4)
	if err := Validate([]byte(mixed), lim); !errors.Is(err, ErrDepth) {
		t.Errorf("expected ErrDepth on mixed nesting, got %v", err)
	}
}

func TestValidate_StringTooLong(t *testing.T) {
	lim := looseLimits
	lim.MaxStrLen = 5

	if err := Validate([]byte("5:hello"), lim); err != nil {
		t.Errorf("len=5 with MaxStrLen=5 should pass, got %v", err)
	}
	err := Validate([]byte("6:hello!"), lim)
	if !errors.Is(err, ErrStringTooLong) {
		t.Errorf("expected ErrStringTooLong, got %v", err)
	}

	// Declared length only — never even reads the bytes.
	err = Validate([]byte("99999999:short"), lim)
	if !errors.Is(err, ErrStringTooLong) {
		t.Errorf("expected ErrStringTooLong on declared-only oversize, got %v", err)
	}
}

func TestValidate_ListTooLong(t *testing.T) {
	lim := looseLimits
	lim.MaxListLen = 3

	if err := Validate([]byte("li1ei2ei3ee"), lim); err != nil {
		t.Errorf("3 items with MaxListLen=3 should pass, got %v", err)
	}
	err := Validate([]byte("li1ei2ei3ei4ee"), lim)
	if !errors.Is(err, ErrListTooLong) {
		t.Errorf("expected ErrListTooLong, got %v", err)
	}
}

func TestValidate_DictTooLarge(t *testing.T) {
	lim := looseLimits
	lim.MaxKeysPerDict = 2

	if err := Validate([]byte("d1:ai1e1:bi2ee"), lim); err != nil {
		t.Errorf("2 keys with MaxKeysPerDict=2 should pass, got %v", err)
	}
	err := Validate([]byte("d1:ai1e1:bi2e1:ci3ee"), lim)
	if !errors.Is(err, ErrDictTooLarge) {
		t.Errorf("expected ErrDictTooLarge, got %v", err)
	}
}

func TestValidate_TooLarge(t *testing.T) {
	lim := looseLimits
	lim.MaxBytes = 4

	if err := Validate([]byte("3:foo"), lim); !errors.Is(err, ErrTooLarge) {
		t.Errorf("expected ErrTooLarge for 5-byte input with MaxBytes=4, got %v", err)
	}
}

func TestValidate_NoAlloc(t *testing.T) {
	// Quick sanity check: a giant declared-but-not-sent string is rejected
	// before we'd allocate anything.
	huge := "9999999999:short"
	if err := Validate([]byte(huge), TorrentLimits); err == nil {
		t.Fatal("expected rejection of declared-only multi-GB string")
	}
}

func TestValidate_PresetLimits(t *testing.T) {
	// Valid simple message under each preset
	simple := []byte("d4:nope0:e")
	for name, lim := range map[string]Limits{
		"TorrentLimits":         TorrentLimits,
		"PeerMessageLimits":     PeerMessageLimits,
		"TrackerResponseLimits": TrackerResponseLimits,
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(simple, lim); err != nil {
				t.Errorf("%s rejected trivial input: %v", name, err)
			}
		})
	}
}
