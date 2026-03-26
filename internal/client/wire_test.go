package client

import (
	"bytes"
	"io"
	"testing"
)

func TestReadMessage(t *testing.T) {
	t.Parallel()
	// 1. KeepAlive message (length 0)
	buf := bytes.NewBuffer([]byte{0, 0, 0, 0})
	m, err := ReadMessage(buf)
	if err != nil {
		t.Fatalf("failed to read keep-alive: %v", err)
	}
	if m != KeepAlive {
		t.Error("expected keep-alive message")
	}

	// 2. Interested message (length 1, ID 2)
	buf = bytes.NewBuffer([]byte{0, 0, 0, 1, 2})
	m, err = ReadMessage(buf)
	if err != nil {
		t.Fatalf("failed to read interested: %v", err)
	}
	if m.ID != MsgInterested || len(m.Payload) != 0 {
		t.Errorf("unexpected message: ID=%d, PayloadLen=%d", m.ID, len(m.Payload))
	}

	// 3. Request message (length 13, ID 6, payload 12 bytes)
	buf = bytes.NewBuffer([]byte{0, 0, 0, 13, 6, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	m, err = ReadMessage(buf)
	if err != nil {
		t.Fatalf("failed to read request: %v", err)
	}
	if m.ID != MsgRequest || len(m.Payload) != 12 {
		t.Errorf("unexpected message: ID=%d, PayloadLen=%d", m.ID, len(m.Payload))
	}
}

func TestWriteMessage(t *testing.T) {
	t.Parallel()
	// 1. KeepAlive
	var buf bytes.Buffer
	err := WriteMessage(&buf, KeepAlive)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), []byte{0, 0, 0, 0}) {
		t.Errorf("unexpected bytes for keep-alive: %x", buf.Bytes())
	}

	// 2. Unchoke
	buf.Reset()
	err = WriteMessage(&buf, &Message{ID: MsgUnchoke})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), []byte{0, 0, 0, 1, 1}) {
		t.Errorf("unexpected bytes for unchoke: %x", buf.Bytes())
	}

	// 3. Request
	buf.Reset()
	m := FormatRequest(1, 2, 16384)
	err = WriteMessage(&buf, m)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	// Expected length: 13 (0x0D)
	// Expected ID: 6
	// Expected Payload: index=1, begin=2, length=16384 (0x4000)
	expected := []byte{
		0, 0, 0, 13, 6,
		0, 0, 0, 1, // index
		0, 0, 0, 2, // begin
		0, 0, 0x40, 0, // length (16384)
	}
	if !bytes.Equal(buf.Bytes(), expected) {
		t.Errorf("unexpected bytes for request: %x, expected %x", buf.Bytes(), expected)
	}
}

func TestReadMessageLargePayload(t *testing.T) {
	t.Parallel()
	// 10MB length prefix
	buf := bytes.NewBuffer([]byte{0, 0x98, 0x96, 0x81, 1})
	_, err := ReadMessage(buf)
	if err == nil {
		t.Error("expected error for large payload")
	}
}

func TestReadMessageEOF(t *testing.T) {
	t.Parallel()
	// Truncated message
	buf := bytes.NewBuffer([]byte{0, 0, 0, 5, 6, 1, 2})
	_, err := ReadMessage(buf)
	if err == nil || err == io.EOF {
		t.Errorf("expected read error, got: %v", err)
	}
}
