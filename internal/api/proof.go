package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// proofWindow is how long a check of an account is open for its player to
// log in as it.
const proofWindow = 10 * time.Minute

// proofs are the checks of Minecraft accounts that people have asked for and
// that are open, by the account's name in lower case: the router has the
// game of a player logging in by that name prove who is playing, and says
// how it went here.
type proofs struct {
	mu     sync.Mutex
	byName map[string]proof
}

type proof struct {
	userID, uuid, name string
	until              time.Time
}

func (p *proofs) open(pr proof) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byName == nil {
		p.byName = map[string]proof{}
	}
	now := time.Now()
	for k, v := range p.byName {
		if now.After(v.until) {
			delete(p.byName, k)
		}
	}
	p.byName[strings.ToLower(pr.name)] = pr
}

// take returns the open check for a name and closes it.
func (p *proofs) take(name string) (proof, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr, ok := p.byName[strings.ToLower(name)]
	delete(p.byName, strings.ToLower(name))
	return pr, ok && time.Now().Before(pr.until)
}

func (p *proofs) pending(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pr, ok := p.byName[strings.ToLower(name)]
	return ok && time.Now().Before(pr.until)
}

// verifyMinecraftAccount opens a check of one of the user's accounts: its
// player logs in as it, at any address of the panel's shared one, within
// proofWindow.
func (s *Server) verifyMinecraftAccount(w http.ResponseWriter, r *http.Request, u *store.User) error {
	all, err := s.Store.MinecraftAccounts(r.Context(), u.ID)
	if err != nil {
		return err
	}
	for _, a := range all {
		if a.UUID != r.PathValue("uuid") {
			continue
		}
		until := time.Now().Add(proofWindow)
		s.proofs.open(proof{userID: u.ID, uuid: a.UUID, name: a.Name, until: until})
		writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "until": until})
		return nil
	}
	return errNotFound
}

// Pending says whether a check is open for a player of this name. It is for
// the router.
func (s *Server) Pending(name string) bool { return s.proofs.pending(name) }

// Proven is told by the router that a player logged in as name and is, as
// Mojang vouches, the account of uuid. It records the account as proven if
// that is the one the check was for, and says what to tell the player.
func (s *Server) Proven(ctx context.Context, name, uuid string) string {
	pr, ok := s.proofs.take(name)
	if !ok {
		return "No check of " + name + " is open. Ask for one from your account in the panel."
	}
	if !strings.EqualFold(pr.uuid, uuid) {
		return "You signed in as another account than the " + pr.name + " being checked."
	}
	if err := s.Store.VerifyMinecraftAccount(ctx, pr.userID, pr.uuid); err != nil {
		s.Log.Warn("could not record a proven Minecraft account", "name", name, "error", err)
		return "Verified, but the panel could not record it. Try again."
	}
	if u, err := s.Store.User(ctx, pr.userID); err == nil {
		s.syncOpsOf(u)
	}
	return "Verified: " + pr.name + " is yours on the panel. Join again to play."
}
