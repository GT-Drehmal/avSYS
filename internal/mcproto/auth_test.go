package mcproto

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

// The hashes the game writes, as published for these three names.
func TestServerHash(t *testing.T) {
	for in, want := range map[string]string{
		"Notch": "4ed1f46bbe04bc756bcb17c0c7ce3e4632f06a48",
		"jeb_":  "-7c9d5b0044c130109a5d7b5fb5c317c02b4e28c1",
		"simon": "88e16a1019277b15d58faf0541e11910eb756f6",
	} {
		if got := ServerHash(in, nil, nil); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// game plays a game's side of an online-mode login: it reads the server's
// key, seals a secret of its own and the token with it, "tells Mojang" the
// hash, and reads what it is told after, decrypted.
func game(t *testing.T, conn net.Conn, protocol int, sendBackToken bool, told *string) (hash string, said Text, err error) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	id, p, _, err := readPacket(r)
	if err != nil || id != 0x01 {
		return "", said, errors.New("no encryption request")
	}
	serverID, _ := readString(p, 20)
	public, _ := readBytes(p, 1024)
	token, _ := readBytes(p, 16)
	if protocol >= 766 {
		if b, err := p.ReadByte(); err != nil || b != 1 {
			return "", said, errors.New("not asked to have Mojang vouch")
		}
	}
	key, err := x509.ParsePKIXPublicKey(public)
	if err != nil {
		return "", said, err
	}
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	sealedSecret, _ := rsa.EncryptPKCS1v15(rand.Reader, key.(*rsa.PublicKey), secret)
	if !sendBackToken {
		token = []byte("nope")
	}
	sealedToken, _ := rsa.EncryptPKCS1v15(rand.Reader, key.(*rsa.PublicKey), token)
	resp := appendBytes(nil, sealedSecret)
	if protocol == 759 || protocol == 760 {
		resp = append(resp, 1)
	}
	resp = appendBytes(resp, sealedToken)
	// What it tells Mojang, before the server asks Mojang.
	hash = ServerHash(serverID, secret, public)
	*told = hash
	if _, err := conn.Write(frame(0x01, resp)); err != nil {
		return "", said, err
	}
	block, _ := aes.NewCipher(secret)
	in := bufio.NewReader(&cipher.StreamReader{S: newCFB8(block, secret, true), R: r})
	id, p, _, err = readPacket(in)
	if err != nil {
		return hash, said, err
	}
	if id != 0x00 {
		return hash, said, errors.New("not a disconnect")
	}
	body, _ := readString(p, maxPacket)
	err = json.Unmarshal([]byte(body), &said)
	return hash, said, err
}

func loginStart(name string, protocol int) []byte {
	hs := appendVarInt(nil, protocol)
	hs = appendString(hs, "play.example.org")
	hs = binary.BigEndian.AppendUint16(hs, 25565)
	hs = appendVarInt(hs, NextLogin)
	login := appendString(nil, name)
	login = append(login, make([]byte, 16)...)
	return append(frame(0x00, hs), frame(0x00, login)...)
}

// A player whose game proves who they are is vouched for by the hash it
// told Mojang, and is told so over the encrypted connection; one that does
// not is not.
func TestAuthenticate(t *testing.T) {
	for _, protocol := range []int{763, 767, 760, 774} {
		for _, honest := range []bool{true, false} {
			server, client := tcpPair(t)
			var told string
			joined := func(_ context.Context, name, hash string) (string, error) {
				if name != "Steve" {
					t.Errorf("asked for %q", name)
				}
				if !honest || hash != told {
					return "", ErrNotJoined
				}
				return "8667ba71b85a4004af54457a9734eed7", nil
			}
			done := make(chan struct{})
			var said Text
			var gameErr error
			go func() {
				defer close(done)
				if _, err := client.Write(loginStart("Steve", protocol)); err != nil {
					gameErr = err
					return
				}
				_, said, gameErr = game(t, client, protocol, true, &told)
			}()
			br := bufio.NewReader(server)
			hs, err := ReadHandshake(br)
			if err != nil {
				t.Fatal(err)
			}
			name, err := LoginName(br)
			if err != nil || name != "Steve" {
				t.Fatalf("login name: %q, %v", name, err)
			}
			uuid, refuse, err := Authenticate(context.Background(), server, br, hs.Protocol, name, joined)
			if honest && (err != nil || uuid != "8667ba71b85a4004af54457a9734eed7") {
				t.Errorf("protocol %d, honest: %q, %v", protocol, uuid, err)
			}
			if !honest && !errors.Is(err, ErrNotJoined) {
				t.Errorf("protocol %d, not vouched for: %q, %v", protocol, uuid, err)
			}
			if refuse == nil {
				t.Fatalf("protocol %d: no way to answer", protocol)
			}
			_ = refuse(Text{Text: "Verified"})
			<-done
			if gameErr != nil || said.Text != "Verified" {
				t.Errorf("protocol %d: the game read %+v, %v", protocol, said, gameErr)
			}
			server.Close()
			client.Close()
		}
	}
}

func tcpPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return <-accepted, client
}

// A game that does not send back the token it was given is not taken.
func TestAuthenticateWrongToken(t *testing.T) {
	server, client := tcpPair(t)
	defer server.Close()
	defer client.Close()
	go func() {
		_, _ = client.Write(loginStart("Steve", 767))
		var told string
		_, _, _ = game(t, client, 767, false, &told)
	}()
	br := bufio.NewReader(server)
	hs, _ := ReadHandshake(br)
	name, _ := LoginName(br)
	asked := false
	_, _, err := Authenticate(context.Background(), server, br, hs.Protocol, name, func(context.Context, string, string) (string, error) {
		asked = true
		return "x", nil
	})
	if err == nil || asked {
		t.Errorf("a wrong token: %v, Mojang asked: %v", err, asked)
	}
}

// The login name is read without being taken, so that a connection not
// handled here goes on whole.
func TestLoginNameLeavesThePacket(t *testing.T) {
	data := loginStart("Alex", 767)
	br := bufio.NewReader(bytes.NewReader(data))
	if _, err := ReadHandshake(br); err != nil {
		t.Fatal(err)
	}
	before := br.Buffered()
	if name, err := LoginName(br); err != nil || name != "Alex" {
		t.Fatalf("%q, %v", name, err)
	}
	if br.Buffered() != before {
		t.Errorf("the packet was taken: %d buffered, was %d", br.Buffered(), before)
	}
}
