package router

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// Bedrock players reach a server through Geyser, which speaks RakNet over
// UDP. Of that, the stand-in for a server whose game is not running needs
// only the messages sent before a connection: the ping of the server list,
// answered with a line about the server, and the first request to connect,
// which is a player joining.

// raknetMagic is in every RakNet message sent outside a connection.
var raknetMagic = []byte{0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe, 0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78}

const (
	raknetPing        = 0x01 // unconnected ping
	raknetPingOpen    = 0x02 // unconnected ping, of a server with room
	raknetOpenRequest = 0x05 // open connection request 1
	raknetPong        = 0x1c // unconnected pong
)

// raknetPingTime reads a server list's ping: the time it was sent, which the
// answer gives back. ok is false for anything else.
//
// A ping is the id, the client's time as 8 bytes, the magic, and the
// client's own id as 8 bytes.
func raknetPingTime(b []byte) (sent []byte, ok bool) {
	if len(b) < 1+8+16 || (b[0] != raknetPing && b[0] != raknetPingOpen) || !bytes.Equal(b[9:25], raknetMagic) {
		return nil, false
	}
	return b[1:9], true
}

// raknetJoin says whether a message is a player's first request to connect:
// the id, the magic, the protocol, and padding up to the size of packet
// tried.
func raknetJoin(b []byte) bool {
	return len(b) >= 1+16+1 && b[0] == raknetOpenRequest && bytes.Equal(b[1:17], raknetMagic)
}

// raknetPongOf answers a ping: the id, the time of the ping, the server's
// id, the magic, and a line about the server.
func raknetPongOf(sent []byte, guid uint64, line string) []byte {
	out := make([]byte, 0, 1+8+8+16+2+len(line))
	out = append(out, raknetPong)
	out = append(out, sent...)
	out = binary.BigEndian.AppendUint64(out, guid)
	out = append(out, raknetMagic...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(line)))
	return append(out, line...)
}

// bedrockStatus is what a Bedrock server says of itself in the server list.
type bedrockStatus struct {
	// Title is the line the list shows, Detail the second, which it shows
	// of servers on the same network.
	Title, Detail string
	// Protocol and Version are the game's, as the server speaks them.
	Protocol int
	Version  string
	Online   int
	Max      int
	GUID     uint64
	// Port is the one the server listens on.
	Port int32
}

// bedrockProtocol and bedrockVersion are what the stand-in says it speaks.
// A server that is not running speaks nothing; the list shows the server
// whatever these are.
const (
	bedrockProtocol = 844
	bedrockVersion  = "1.21.111"
)

// line is the status as a pong carries it: fields split by semicolons.
func (s bedrockStatus) line() string {
	clean := strings.NewReplacer(";", ",", "\n", " ", "\r", " ").Replace
	return fmt.Sprintf("MCPE;%s;%d;%s;%d;%d;%d;%s;Survival;1;%d;%d;",
		clean(s.Title), s.Protocol, clean(s.Version), s.Online, s.Max, s.GUID, clean(s.Detail), s.Port, s.Port)
}
