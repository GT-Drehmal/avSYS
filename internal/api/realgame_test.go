//go:build realgame

package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/rcon"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A real game in place of the test's: a server in offline mode with its
// whitelist off and enforce-whitelist on, reached over RCON at
// AVSYS_REALGAME_RCON with AVSYS_REALGAME_PASSWORD. Players Bystander and
// Keeper have joined it once, so that it knows them, and Visitor, on no
// list, is in it. Not run by go test ./...:
//
//	go test -tags realgame -run RealGame ./internal/api/
type realGame struct{ addr string }

func (g realGame) DialPod(ctx context.Context, _, _ string, _ int) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", g.addr)
}

// Taking a member off the whitelist of a server whose whitelist is off puts
// nobody out; turning the whitelist on takes them off.
func TestRealGameKeepsVisitorsWhileTheWhitelistIsOff(t *testing.T) {
	addr, password := os.Getenv("AVSYS_REALGAME_RCON"), os.Getenv("AVSYS_REALGAME_PASSWORD")
	if addr == "" {
		t.Skip("no real game: set AVSYS_REALGAME_RCON")
	}
	ctx := context.Background()
	p := newPanel(t)
	runningWithLists(t, p)
	p.srv.Kube = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world-rcon"},
		Data:       map[string][]byte{"password": []byte(password)},
	})
	p.srv.Dialer = realGame{addr}
	// As the panel reads a server again before it changes its lists.
	p.srv.Reader = p.srv.Client
	p.srv.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	game := func(command string) string {
		t.Helper()
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		out, err := rcon.Run(conn, password, command)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	in := func() bool { return strings.Contains(game("list"), "Visitor") }
	if !in() {
		t.Fatal("Visitor is not in the game to begin with")
	}
	// Ada (admin) plays as Bystander, Olive (owner) as Keeper.
	for who, name := range map[string]string{"admin": "Bystander", "owner": "Keeper"} {
		if err := p.srv.Store.SetMinecraftAccount(ctx, p.users[who].ID, &store.MinecraftAccount{UUID: strings.Repeat(strings.ToLower(name[:1]), 32), Name: name, Main: true}); err != nil {
			t.Fatal(err)
		}
	}
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp, mc.Spec.AutoWhitelist = "off", "admin"
		mc.Spec.Properties = map[string]string{"white-list": "false", "enforce-whitelist": "true"}
	})
	org, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	p.srv.syncOrgOps(ctx, org)
	t.Logf("whitelist after the members are put on: %s", game("whitelist list"))
	if got := game("whitelist list"); !strings.Contains(got, "Bystander") || !strings.Contains(got, "Keeper") {
		t.Fatalf("not put on: %s", got)
	}
	if !in() {
		t.Fatal("Visitor was put out as the members were put on")
	}

	// Bystander's player leaves the organization.
	if code, msg := p.do("owner", "DELETE", "/orgs/alpha/members/"+p.users["admin"].ID, ""); code != http.StatusNoContent {
		t.Fatalf("removing the member: %d %s", code, msg)
	}
	time.Sleep(2 * time.Second)
	p.srv.syncOrgOps(ctx, org)
	time.Sleep(2 * time.Second)
	t.Logf("with the whitelist off, after the member left: list %q, whitelist %q", game("list"), game("whitelist list"))
	if !in() {
		t.Fatal("Visitor was put out when a member left, with the whitelist off")
	}
	if got := game("whitelist list"); !strings.Contains(got, "Bystander") {
		t.Errorf("taken off with the whitelist off: %s", got)
	}

	// The whitelist turned on: Bystander comes off at once.
	if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"properties":{"white-list":"true"}}}`); code != http.StatusOK {
		t.Fatalf("turning the whitelist on: %d %s", code, msg)
	}
	// Waited for by the panel's note of whom it put on, not by asking the
	// game: two RCON connections asking at once can get each other's output.
	noted := func() string { return world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist] }
	for end := time.Now().Add(time.Minute); strings.Contains(noted(), "Bystander") && time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
	}
	t.Logf("the panel's note once the whitelist is on: %q", noted())
	got := game("whitelist list")
	t.Logf("with the whitelist on: list %q, whitelist %q", game("list"), got)
	if strings.Contains(got, "Bystander") {
		t.Errorf("the member who left was not taken off once the whitelist was on: %s", got)
	}
	if !strings.Contains(got, "Keeper") {
		t.Errorf("a member still in was taken off: %s", got)
	}
}
