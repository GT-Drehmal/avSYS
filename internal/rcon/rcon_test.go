package rcon

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// game answers RCON as Minecraft does: the password first, then a command's
// output in pieces of at most 4096 bytes, then, in order, "Unknown request"
// for a packet of a type it does not know.
func game(t *testing.T, password string, output func(command string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				id, body, err := read(conn)
				if err != nil {
					return
				}
				if body != password {
					_ = write(conn, -1, 2, "")
					return
				}
				_ = write(conn, id, 2, "")
				for {
					id, body, err := read(conn)
					if err != nil {
						return
					}
					if id == 3 {
						_ = write(conn, id, 0, "Unknown request 64")
						continue
					}
					out := output(body)
					for {
						piece := out
						if len(piece) > 4096 {
							piece = piece[:4096]
						}
						_ = write(conn, id, 0, piece)
						out = out[len(piece):]
						if out == "" {
							break
						}
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c
}

func TestRun(t *testing.T) {
	long := strings.Repeat("§aa line of a long list\n", 1000)
	addr := game(t, "secret", func(command string) string {
		switch command {
		case "list":
			return "There are 0 of a max of 20 players online: "
		case "long":
			return long
		case "nothing":
			return ""
		}
		return "Unknown or incomplete command"
	})
	for command, want := range map[string]string{"list": "There are 0 of a max of 20 players online: ", "long": long, "nothing": "", "op Steve and more": "Unknown or incomplete command"} {
		got, err := Run(dial(t, addr), "secret", command)
		if err != nil || got != want {
			t.Errorf("%s: %d bytes, %v; want %d", command, len(got), err, len(want))
		}
	}
	if _, err := Run(dial(t, addr), "wrong", "list"); !errors.Is(err, ErrAuth) {
		t.Errorf("a wrong password: %v", err)
	}
}

// A game that goes away part way, or answers with nonsense, is an error, not
// a hang or a short answer taken for the whole.
func TestRunBrokenGame(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for i := 0; ; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			id, _, _ := read(conn)
			_ = write(conn, id, 2, "")
			switch i {
			case 0:
				// Gone before the command's output.
			case 1:
				// A first piece, then gone before the end is marked.
				id, _, _ := read(conn)
				_ = write(conn, id, 0, "part of it")
			case 2:
				// A size no packet has.
				_, _ = conn.Write([]byte{0xff, 0xff, 0xff, 0x7f, 0, 0, 0, 0, 0, 0, 0, 0})
			}
			conn.Close()
		}
	}()
	for i := range 3 {
		if out, err := Run(dial(t, ln.Addr().String()), "secret", "list"); err == nil {
			t.Errorf("case %d: %q, no error", i, out)
		}
	}
}
