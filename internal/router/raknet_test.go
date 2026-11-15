package router

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A server list's ping as Bedrock sends it: the id, its time, the magic,
// and its own id.
const pingBytes = `01 000000000003b6e4 00ffff00fefefefefdfdfdfd12345678 d2c1a3f04e7b9a11`

// A Bedrock Dedicated Server's answer to it: the id, the ping's time, the
// server's id, the magic, and its line, of 0x61 bytes.
const pongBytes = `1c 000000000003b6e4 b7ef2c4275b52e31 00ffff00fefefefefdfdfdfd12345678 0061
	4d4350453b446564696361746564205365727665723b3339303b312e31342e36303b303b31303b31333235333836303839323332383933303836353b
	426564726f636b206c6576656c3b537572766976616c3b313b31393133323b31393133323b`

// openRequest is a player joining: the id, the magic, RakNet's protocol,
// and padding to the size of packet tried first.
func openRequest(t *testing.T) []byte {
	b := unhex(t, `05 00ffff00fefefefefdfdfdfd12345678 0b`)
	return append(b, make([]byte, 1464-len(b))...)
}

func TestRakNetPing(t *testing.T) {
	sent, ok := raknetPingTime(unhex(t, pingBytes))
	if !ok || !bytes.Equal(sent, unhex(t, "000000000003b6e4")) {
		t.Fatalf("ping not read: %x %v", sent, ok)
	}
	withRoom := unhex(t, pingBytes)
	withRoom[0] = raknetPingOpen
	if _, ok := raknetPingTime(withRoom); !ok {
		t.Error("a ping for servers with room not read")
	}
	for name, b := range map[string][]byte{
		"short":          unhex(t, pingBytes)[:24],
		"no magic":       append([]byte{0x01}, make([]byte, 32)...),
		"a join":         openRequest(t),
		"not RakNet":     []byte("hello"),
		"nothing at all": nil,
	} {
		if _, ok := raknetPingTime(b); ok {
			t.Errorf("%s taken as a ping", name)
		}
	}
}

func TestRakNetJoin(t *testing.T) {
	if !raknetJoin(openRequest(t)) {
		t.Error("a join not seen")
	}
	if raknetJoin(unhex(t, pingBytes)) || raknetJoin([]byte{0x05, 0x00}) || raknetJoin(nil) {
		t.Error("something else taken as a join")
	}
}

// The answer is byte for byte what Bedrock's own server sends.
func TestRakNetPong(t *testing.T) {
	sent, _ := raknetPingTime(unhex(t, pingBytes))
	st := bedrockStatus{Title: "Dedicated Server", Detail: "Bedrock level", Protocol: 390, Version: "1.14.60", Online: 0, Max: 10, GUID: 13253860892328930865, Port: 19132}
	if got, want := raknetPongOf(sent, st.GUID, st.line()), unhex(t, pongBytes); !bytes.Equal(got, want) {
		t.Errorf("pong\n got %x\nwant %x", got, want)
	}
}

// What a server is called cannot break up its line, whose fields are split
// by semicolons.
func TestBedrockLineEscapes(t *testing.T) {
	line := bedrockStatus{Title: "Asleep; join", Detail: "Club;\nEvent", Protocol: 1, Version: "1", Max: 20, Port: 19132}.line()
	if n := strings.Count(line, ";"); n != 12 {
		t.Errorf("%d fields in %q", n, line)
	}
}
