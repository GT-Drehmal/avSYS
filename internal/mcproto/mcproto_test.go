package mcproto

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
)

func TestHostnameNormalisation(t *testing.T) {
	for host, want := range map[string]string{
		"Play.Example.ORG":             "play.example.org",
		"play.example.org.":            "play.example.org",
		"play.example.org\x00FML3\x00": "play.example.org",
		"mc.example.com\x00FML2\x00":   "mc.example.com",
	} {
		if got := (Handshake{Host: host}).Hostname(); got != want {
			t.Errorf("Hostname(%q) = %q, want %q", host, got, want)
		}
	}
}

// Ping and ServeStatus are each other's counterpart, so one exercises the other.
func TestPingAgainstServeStatus(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		hs, err := ReadHandshake(r)
		if err != nil || hs.NextState != NextStatus || hs.Hostname() != "play.example.com" {
			t.Errorf("handshake = %+v, err %v", hs, err)
			return
		}
		desc, _ := json.Marshal(Text{Text: "hello"})
		_ = ServeStatus(server, r, Status{
			Version:     Version{Name: "avSYS", Protocol: 767},
			Players:     Players{Online: 3, Max: 20, Sample: []Player{{Name: "Steve"}}},
			Description: desc,
		})
	}()

	st, err := Ping(client, "play.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if st.Players.Online != 3 || st.Players.Max != 20 || st.Players.Sample[0].Name != "Steve" || st.Version.Name != "avSYS" {
		t.Errorf("status = %+v", st)
	}
}

func TestReadHandshakeRejectsLegacyPing(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		_, _ = client.Write([]byte{0xFE, 0x01})
		client.Close()
	}()
	if _, err := ReadHandshake(bufio.NewReader(server)); err != ErrLegacyPing {
		t.Errorf("err = %v, want ErrLegacyPing", err)
	}
}
