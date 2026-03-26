package client

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Standard BEP 3 Message IDs
const (
	MsgChoke         byte = 0
	MsgUnchoke       byte = 1
	MsgInterested    byte = 2
	MsgNotInterested byte = 3
	MsgHave          byte = 4
	MsgBitfield      byte = 5
	MsgRequest       byte = 6
	MsgPiece         byte = 7
	MsgCancel        byte = 8
	MsgExtended      byte = 20 // BEP 10
)

// Message represents a parsed BitTorrent message.
type Message struct {
	ID      byte
	Payload []byte
}

// KeepAlive represents the special 0-length keep-alive message.
var KeepAlive = (*Message)(nil)

// ReadMessage reads a single length-prefixed message from the wire.
func ReadMessage(r io.Reader) (*Message, error) {
	// Length prefix (4 bytes)
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read message length: %w", err)
	}

	// Keep-Alive message (length 0)
	if length == 0 {
		return KeepAlive, nil
	}

	// Message ID (1 byte)
	idBuf := make([]byte, 1)
	if _, err := io.ReadFull(r, idBuf); err != nil {
		return nil, fmt.Errorf("read message id: %w", err)
	}

	// Payload (length - 1 bytes)
	payloadLen := length - 1
	if payloadLen == 0 {
		return &Message{ID: idBuf[0], Payload: nil}, nil
	}

	// Protect against massive arbitrary allocations
	if payloadLen > 2<<20 { // 2MB max payload
		return nil, fmt.Errorf("message payload too large: %d bytes", payloadLen)
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("read message payload: %w", err)
	}

	return &Message{ID: idBuf[0], Payload: payload}, nil
}

// WriteMessage writes a message to the wire.
func WriteMessage(w io.Writer, m *Message) error {
	if m == KeepAlive {
		_, err := w.Write([]byte{0, 0, 0, 0})
		return err
	}

	// Length = 1 byte for ID + length of payload
	length := uint32(1 + len(m.Payload))

	buf := make([]byte, 4+length)
	binary.BigEndian.PutUint32(buf[0:4], length)
	buf[4] = m.ID
	copy(buf[5:], m.Payload)

	_, err := w.Write(buf)
	return err
}

// FormatRequest creates a BEP 3 Request message payload (index, begin, length).
func FormatRequest(index, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgRequest, Payload: payload}
}
