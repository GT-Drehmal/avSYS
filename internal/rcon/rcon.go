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

func read(r io.Reader) (id, typ int32, body string, err error) {
	var head [12]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, 0, "", err
	}
	size := int(binary.LittleEndian.Uint32(head[0:4]))
	if size < 10 || size > maxBody {
		return 0, 0, "", fmt.Errorf("rcon: bad packet size %d", size)
	}
	rest := make([]byte, size-8)
	if _, err := io.ReadFull(r, rest); err != nil {
		return 0, 0, "", err
	}
	return int32(binary.LittleEndian.Uint32(head[4:8])), int32(binary.LittleEndian.Uint32(head[8:12])), string(rest[:len(rest)-2]), nil
}

// Client is a connection the game has taken the password on, for one
// command after another. It is not for two to use at once, and after an
// error it is to be closed: what is left unread of one command's output
// would be taken for the next one's.
type Client struct {
	conn net.Conn
	id   int32
}

// Login authenticates on conn.
func Login(conn net.Conn, password string) (*Client, error) {
	if err := write(conn, 1, typeAuth, password); err != nil {
		return nil, err
	}
	id, _, _, err := read(conn)
	if err != nil {
		return nil, err
	}
	if id == -1 {
		return nil, ErrAuth
	}
	return &Client{conn: conn}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Command runs one command and returns its output.
func (c *Client) Command(command string) (string, error) {
	// Long output is split across packets with nothing marking the last one.
	// The server answers an unknown packet type in order, so a sentinel sent
	// after the command marks where the command's output ends. The server
	// drops the connection if two packets arrive in one read, so the sentinel
	// only goes out once the first piece of output has come back.
	c.id += 2
	cmd, sentinel := c.id, c.id+1
	if err := write(c.conn, cmd, typeCommand, command); err != nil {
		return "", err
	}
	_, _, first, err := read(c.conn)
	if err != nil {
		return "", err
	}
	if err := write(c.conn, sentinel, typeSentinel, ""); err != nil {
		return first, err
	}
	out := strings.Builder{}
	out.WriteString(first)
	for {
		id, _, body, err := read(c.conn)
		if err != nil {
			return out.String(), err
		}
		if id == sentinel {
			return out.String(), nil
		}
		out.WriteString(body)
	}
}

// Run authenticates on conn, runs one command and returns its output.
func Run(conn net.Conn, password, command string) (string, error) {
	c, err := Login(conn, password)
	if err != nil {
		return "", err
	}
	return c.Command(command)
}
