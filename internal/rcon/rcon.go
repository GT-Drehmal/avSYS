// Package rcon is a minimal client for the Minecraft RCON protocol.
package rcon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

const (
	typeAuth     = 3
	typeCommand  = 2
	typeSentinel = 100

	maxBody = 1 << 20
)

var ErrAuth = errors.New("rcon: wrong password")

func write(w io.Writer, id, typ int32, body string) error {
	buf := make([]byte, 0, 14+len(body))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(10+len(body)))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(id))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(typ))
	buf = append(buf, body...)
	buf = append(buf, 0, 0)
	_, err := w.Write(buf)
	return err
}

func read(r io.Reader) (id int32, body string, err error) {
	var head [12]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, "", err
	}
	size := int(binary.LittleEndian.Uint32(head[0:4]))
	if size < 10 || size > maxBody {
		return 0, "", fmt.Errorf("rcon: bad packet size %d", size)
	}
	rest := make([]byte, size-8)
	if _, err := io.ReadFull(r, rest); err != nil {
		return 0, "", err
	}
	return int32(binary.LittleEndian.Uint32(head[4:8])), string(rest[:len(rest)-2]), nil
}

// Run authenticates on conn, runs one command and returns its output.
func Run(conn net.Conn, password, command string) (string, error) {
	if err := write(conn, 1, typeAuth, password); err != nil {
		return "", err
	}
	id, _, err := read(conn)
	if err != nil {
		return "", err
	}
	if id == -1 {
		return "", ErrAuth
	}

	// Long output is split across packets with nothing marking the last one.
	// The server answers an unknown packet type in order, so a sentinel sent
	// after the command marks where the command's output ends. The server
	// drops the connection if two packets arrive in one read, so the sentinel
	// only goes out once the first piece of output has come back.
	if err := write(conn, 2, typeCommand, command); err != nil {
		return "", err
	}
	_, first, err := read(conn)
	if err != nil {
		return "", err
	}
	if err := write(conn, 3, typeSentinel, ""); err != nil {
		return first, err
	}
	out := strings.Builder{}
	out.WriteString(first)
	for {
		id, body, err := read(conn)
		if err != nil {
			return out.String(), err
		}
		if id == 3 {
			return out.String(), nil
		}
		out.WriteString(body)
	}
}
