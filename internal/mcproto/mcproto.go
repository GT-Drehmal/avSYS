// Package mcproto implements the small slice of the Minecraft Java protocol
// avSYS needs: reading the handshake to route by hostname, answering and
// sending server list pings, and refusing a login with a message.
package mcproto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	NextStatus = 1
	NextLogin  = 2
	// NextTransfer is used by clients arriving via a transfer packet (1.20.5+).
	NextTransfer = 3

	maxPacket = 1 << 21
)

var ErrLegacyPing = errors.New("legacy (pre-1.7) server list ping")

func readVarInt(r io.ByteReader) (int, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return int(int32(v)), nil
		}
	}
	return 0, errors.New("varint too long")
}

func appendVarInt(b []byte, v int) []byte {
	u := uint32(int32(v))
	for u >= 0x80 {
		b = append(b, byte(u)|0x80)
		u >>= 7
	}
	return append(b, byte(u))
}

func appendString(b []byte, s string) []byte {
	b = appendVarInt(b, len(s))
	return append(b, s...)
}

func readString(r *bytes.Reader, max int) (string, error) {
	n, err := readVarInt(r)
	if err != nil {
		return "", err
	}
	if n < 0 || n > max || n > r.Len() {
		return "", fmt.Errorf("bad string length %d", n)
	}
	buf := make([]byte, n)
	_, _ = io.ReadFull(r, buf)
	return string(buf), nil
}

// frame prefixes a packet body (id + payload) with its length.
func frame(id int, payload []byte) []byte {
	body := appendVarInt(nil, id)
	body = append(body, payload...)
	return append(appendVarInt(nil, len(body)), body...)
}

func readPacket(r *bufio.Reader) (id int, payload *bytes.Reader, raw []byte, err error) {
	n, err := readVarInt(r)
	if err != nil {
		return 0, nil, nil, err
	}
	if n <= 0 || n > maxPacket {
		return 0, nil, nil, fmt.Errorf("bad packet length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, nil, err
	}
	payload = bytes.NewReader(body)
	if id, err = readVarInt(payload); err != nil {
		return 0, nil, nil, err
	}
	return id, payload, append(appendVarInt(nil, n), body...), nil
}

type Handshake struct {
	Protocol  int
	Host      string
	Port      uint16
	NextState int
	// Raw is the packet exactly as received, for replaying to a backend.
	Raw []byte
}

// Hostname is Host normalised for routing: lower-cased, without the trailing
// dot, and without the data Forge (\x00FML...) and SRV-resolving clients append.
func (h Handshake) Hostname() string {
	host, _, _ := strings.Cut(h.Host, "\x00")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func ReadHandshake(r *bufio.Reader) (Handshake, error) {
	if first, err := r.Peek(1); err != nil {
		return Handshake{}, err
	} else if first[0] == 0xFE {
		return Handshake{}, ErrLegacyPing
	}
	id, p, raw, err := readPacket(r)
	if err != nil {
		return Handshake{}, err
	}
	if id != 0 {
		return Handshake{}, fmt.Errorf("expected handshake, got packet %#x", id)
	}
	h := Handshake{Raw: raw}
	if h.Protocol, err = readVarInt(p); err != nil {
		return h, err
	}
	if h.Host, err = readString(p, 255*4); err != nil {
		return h, err
	}
	if err = binary.Read(p, binary.BigEndian, &h.Port); err != nil {
		return h, err
	}
	if h.NextState, err = readVarInt(p); err != nil {
		return h, err
	}
	return h, nil
}

type Version struct {
	Name     string `json:"name"`
	Protocol int    `json:"protocol"`
}

type Player struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type Players struct {
	Max    int      `json:"max"`
	Online int      `json:"online"`
	Sample []Player `json:"sample,omitempty"`
}

// Status is the server list ping response.
type Status struct {
	Version     Version         `json:"version"`
	Players     Players         `json:"players"`
	Description json.RawMessage `json:"description"`
	Favicon     string          `json:"favicon,omitempty"`
}

// Text is a minimal chat component.
type Text struct {
	Text  string `json:"text"`
	Color string `json:"color,omitempty"`
	Bold  bool   `json:"bold,omitempty"`
	Extra []Text `json:"extra,omitempty"`
}

// ServeStatus answers a status request (and the ping that follows it) on a
// connection whose handshake has already been read.
func ServeStatus(conn net.Conn, r *bufio.Reader, st Status) error {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	for {
		id, p, _, err := readPacket(r)
		if err != nil {
			return err
		}
		switch id {
		case 0x00:
			body, err := json.Marshal(st)
			if err != nil {
				return err
			}
			if _, err := conn.Write(frame(0x00, appendString(nil, string(body)))); err != nil {
				return err
			}
		case 0x01:
			rest, _ := io.ReadAll(p)
			_, err := conn.Write(frame(0x01, rest))
			return err
		default:
			return fmt.Errorf("unexpected status packet %#x", id)
		}
	}
}

// Disconnect refuses a login with a message shown on the client's
// "Connection Lost" screen.
func Disconnect(conn net.Conn, msg Text) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write(frame(0x00, appendString(nil, string(body))))
	return err
}

// Ping performs a server list ping over conn and closes nothing.
func Ping(conn net.Conn, host string) (Status, error) {
	var st Status
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetDeadline(time.Time{})

	hs := appendVarInt(nil, -1) // protocol version: unknown, ask what the server speaks
	hs = appendString(hs, host)
	hs = binary.BigEndian.AppendUint16(hs, 25565)
	hs = appendVarInt(hs, NextStatus)
	if _, err := conn.Write(append(frame(0x00, hs), frame(0x00, nil)...)); err != nil {
		return st, err
	}
	id, p, _, err := readPacket(bufio.NewReader(conn))
	if err != nil {
		return st, err
	}
	if id != 0 {
		return st, fmt.Errorf("unexpected status response packet %#x", id)
	}
	body, err := readString(p, maxPacket)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal([]byte(body), &st)
}
