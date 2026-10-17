package mcproto

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

// The game proves who a player is the way an online-mode server has it do:
// the server sends a public key, the game sends back a secret it chose,
// sealed with that key, and tells Mojang it is joining a server named by a
// hash of both. The server then asks Mojang whether that player joined that
// hash; Mojang only says yes for the account's owner, signed in. From there
// the connection is encrypted with the secret.

// LoginName is the player name in the login packet that follows a login
// handshake, read without taking it from r, so that a connection not
// handled here can be passed on whole.
func LoginName(r *bufio.Reader) (string, error) {
	head, err := r.Peek(3)
	if err != nil && len(head) == 0 {
		return "", err
	}
	size, n := 0, 0
	for shift := 0; n < len(head); shift += 7 {
		b := head[n]
		n++
		size |= int(b&0x7f) << shift
		if b&0x80 == 0 {
			break
		}
		if n == len(head) {
			return "", errors.New("login packet length too long")
		}
	}
	if size < 2 || size > 1024 {
		return "", fmt.Errorf("login packet of %d bytes", size)
	}
	packet, err := r.Peek(n + size)
	if err != nil {
		return "", err
	}
	p := bytes.NewReader(packet[n:])
	id, err := readVarInt(p)
	if err != nil {
		return "", err
	}
	if id != 0 {
		return "", fmt.Errorf("expected login start, got packet %#x", id)
	}
	return readString(p, 16*4)
}

// HasJoined asks Mojang whether the player of this name joined the server of
// this hash, and returns their account's UUID, without dashes, if they did.
type HasJoined func(ctx context.Context, name, serverHash string) (uuid string, err error)

// ErrNotJoined is returned for a player Mojang does not vouch for: not the
// account's owner, not signed in, or a game that does not ask Mojang.
var ErrNotJoined = errors.New("mojang did not vouch for the player")

// Authenticate has the game prove who is playing, on a connection whose
// handshake and login packet have been read from r, for a game speaking
// protocol. It returns the account's UUID as Mojang has it, and a way to
// turn the player away with a message, which is all that is left to do on
// the connection: from here it is encrypted.
func Authenticate(ctx context.Context, conn net.Conn, r *bufio.Reader, protocol int, name string, joined HasJoined) (uuid string, refuse func(Text) error, err error) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return "", nil, err
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", nil, err
	}
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		return "", nil, err
	}
	// The login packet itself, which was only looked at, is taken now.
	if _, _, _, err := readPacket(r); err != nil {
		return "", nil, err
	}
	req := appendString(nil, "")
	req = appendBytes(req, public)
	req = appendBytes(req, token)
	if protocol >= 766 {
		// 1.20.5 on: that the game is to have Mojang vouch for it.
		req = append(req, 1)
	}
	if _, err := conn.Write(frame(0x01, req)); err != nil {
		return "", nil, err
	}
	id, p, _, err := readPacket(r)
	if err != nil {
		return "", nil, err
	}
	if id != 0x01 {
		return "", nil, fmt.Errorf("expected encryption response, got packet %#x", id)
	}
	sealedSecret, err := readBytes(p, 256)
	if err != nil {
		return "", nil, err
	}
	// 1.19 to 1.19.2 say whether a token or a signature follows; only a
	// token is taken.
	if protocol == 759 || protocol == 760 {
		if has, err := p.ReadByte(); err != nil || has != 1 {
			return "", nil, errors.New("a signed login is not taken; only a verify token")
		}
	}
	sealedToken, err := readBytes(p, 256)
	if err != nil {
		return "", nil, err
	}
	secret, err := rsa.DecryptPKCS1v15(nil, key, sealedSecret)
	if err != nil || len(secret) != 16 {
		return "", nil, errors.New("the game's secret could not be read")
	}
	back, err := rsa.DecryptPKCS1v15(nil, key, sealedToken)
	if err != nil || subtle.ConstantTimeCompare(back, token) != 1 {
		return "", nil, errors.New("the game did not send back the token it was given")
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", nil, err
	}
	out := &cipher.StreamWriter{S: newCFB8(block, secret, false), W: conn}
	refuse = func(msg Text) error {
		body, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		_, err = out.Write(frame(0x00, appendString(nil, string(body))))
		return err
	}
	uuid, err = joined(ctx, name, ServerHash("", secret, public))
	if err != nil {
		return "", refuse, err
	}
	return uuid, refuse, nil
}

// ServerHash is the name of a server the game tells Mojang it joins: the
// SHA-1 of the server's ID, the shared secret and the server's public key,
// read as one signed number in hexadecimal, as the game writes it.
func ServerHash(serverID string, secret, public []byte) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write(secret)
	h.Write(public)
	sum := h.Sum(nil)
	n := new(big.Int).SetBytes(sum)
	if sum[0]&0x80 != 0 {
		// Negative: the two's complement, with a minus sign.
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(len(sum)*8)))
	}
	return n.Text(16)
}

func appendBytes(b, data []byte) []byte {
	b = appendVarInt(b, len(data))
	return append(b, data...)
}

func readBytes(r *bytes.Reader, max int) ([]byte, error) {
	n, err := readVarInt(r)
	if err != nil {
		return nil, err
	}
	if n < 0 || n > max {
		return nil, fmt.Errorf("byte array of %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// cfb8 is AES in CFB mode eight bits at a time, as the game encrypts its
// connection, with the shared secret as both the key and the IV.
type cfb8 struct {
	block   cipher.Block
	iv, out []byte
	decrypt bool
}

func newCFB8(block cipher.Block, iv []byte, decrypt bool) cipher.Stream {
	return &cfb8{block: block, iv: bytes.Clone(iv), out: make([]byte, block.BlockSize()), decrypt: decrypt}
}

func (c *cfb8) XORKeyStream(dst, src []byte) {
	for i := range src {
		c.block.Encrypt(c.out, c.iv)
		in := src[i]
		dst[i] = in ^ c.out[0]
		feedback := dst[i]
		if c.decrypt {
			feedback = in
		}
		copy(c.iv, c.iv[1:])
		c.iv[len(c.iv)-1] = feedback
	}
}
