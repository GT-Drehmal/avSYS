package api

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/router"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// proofWindow is how long a check of an account is open for its player to
// log in as it, and how long the code they are then shown can be entered.
const proofWindow = 10 * time.Minute

// proofTries is how many wrong codes a check takes before it is closed.
const proofTries = 5

// A check of a Minecraft account goes in two steps. A person asks for one,
// and joins the panel's own address (not a server's) in the game, as the
// account. The router has the game prove who is playing, as an online-mode
// server does, and Mojang vouches only for the account's owner, signed in.
// The game is then shown a code, which the person enters in the panel. Only
// whoever is playing as the account sees the code, so it is credited to the
// panel user who has it: a check opened by someone else for another's name
// gets them nothing, even when that player joins while it is open.

// proofs are the checks people have asked for, and the codes given out to
// players who proved who they are.
type proofs struct {
	mu sync.Mutex
	// byName are the open checks, by the account's name in lower case and
	// then by who asked.
	byName map[string]map[string]*proof
	// codes are those given out, by the code.
	codes map[string]proven
}

type proof struct {
	uuid, name string
	until      time.Time
	wrong      int
}

type proven struct {
	uuid  string
	until time.Time
}

// tidy drops what has run out. It is called with mu held.
func (p *proofs) tidy() {
	now := time.Now()
	for name, by := range p.byName {
		for user, pr := range by {
			if now.After(pr.until) {
				delete(by, user)
			}
		}
		if len(by) == 0 {
			delete(p.byName, name)
		}
	}
	for code, c := range p.codes {
		if now.After(c.until) {
			delete(p.codes, code)
		}
	}
}

func (p *proofs) open(userID string, pr proof) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byName == nil {
		p.byName = map[string]map[string]*proof{}
	}
	p.tidy()
	name := strings.ToLower(pr.name)
	if p.byName[name] == nil {
		p.byName[name] = map[string]*proof{}
	}
	p.byName[name][userID] = &pr
}

func (p *proofs) pending(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tidy()
	return len(p.byName[strings.ToLower(name)]) > 0
}

// give hands out a code for an account a player proved is theirs, if a check
// of that account by that name is open. found says whether any check of the
// name is.
func (p *proofs) give(name, uuid string) (code string, found bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tidy()
	by := p.byName[strings.ToLower(name)]
	for _, pr := range by {
		if strings.EqualFold(pr.uuid, uuid) {
			if p.codes == nil {
				p.codes = map[string]proven{}
			}
			code = newProofCode()
			p.codes[code] = proven{uuid: pr.uuid, until: time.Now().Add(proofWindow)}
			return code, true
		}
	}
	return "", len(by) > 0
}

// redeem takes a code entered by a user for one of their accounts. It is
// right if it was given out for that account while the user's check of it
// was open; then the code and the check are used up. Each wrong one counts
// against the check, which is closed after proofTries.
func (p *proofs) redeem(userID, name, uuid, code string) (ok, open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tidy()
	key := strings.ToLower(name)
	pr := p.byName[key][userID]
	if pr == nil || !strings.EqualFold(pr.uuid, uuid) {
		return false, false
	}
	code = normalProofCode(code)
	if c, given := p.codes[code]; given && strings.EqualFold(c.uuid, uuid) {
		delete(p.codes, code)
		delete(p.byName[key], userID)
		return true, false
	}
	pr.wrong++
	if pr.wrong >= proofTries {
		delete(p.byName[key], userID)
		return false, false
	}
	return false, true
}

// proofAlphabet has no letters that are easily taken for others.
const proofAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newProofCode is eight letters from proofAlphabet, as two groups of four.
func newProofCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 9)
	for i, c := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, proofAlphabet[int(c)%len(proofAlphabet)])
	}
	return string(out)
}

// normalProofCode is a code as it was typed, in the form it was given out:
// in capitals, with what is not one of its letters left out.
func normalProofCode(code string) string {
	var out []byte
	for _, c := range strings.ToUpper(code) {
		if strings.ContainsRune(proofAlphabet, c) {
			out = append(out, byte(c))
		}
	}
	if len(out) != 8 {
		return ""
	}
	return string(out[:4]) + "-" + string(out[4:])
}

// verifyMinecraftAccount opens a check of one of the user's accounts, or,
// with a code, finishes one.
func (s *Server) verifyMinecraftAccount(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct {
		Code string `json:"code"`
	}
	// Opening a check takes no body.
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			return err
		}
	}
	all, err := s.Store.MinecraftAccounts(r.Context(), u.ID)
	if err != nil {
		return err
	}
	for _, a := range all {
		if a.UUID != r.PathValue("uuid") {
			continue
		}
		if in.Code == "" {
			until := time.Now().Add(proofWindow)
			s.proofs.open(u.ID, proof{uuid: a.UUID, name: a.Name, until: until})
			writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "until": until, "address": s.verifyAddress()})
			return nil
		}
		ok, open := s.proofs.redeem(u.ID, a.Name, a.UUID, in.Code)
		if !ok {
			if !open {
				return &Error{Status: http.StatusConflict, Code: "no_check", Message: "No check is open for " + a.Name + ". Click Verify again."}
			}
			return &Error{Status: http.StatusBadRequest, Code: "wrong_code", Message: "Wrong code. Try again."}
		}
		if err := s.Store.VerifyMinecraftAccount(r.Context(), u.ID, a.UUID); err != nil {
			return err
		}
		s.syncOpsOf(u)
		writeJSON(w, http.StatusOK, map[string]any{"name": a.Name, "verified": true})
		return nil
	}
	return errNotFound
}

// verifyAddress is the address players join to prove an account is theirs:
// one under the router's domain that no server has. Without a domain it is
// left to the page to say.
func (s *Server) verifyAddress() string {
	if d := router.NormalizeHostname(s.Config.RouterDomain); d != "" {
		return "verify." + d
	}
	return ""
}

// Pending says whether a check is open for a player of this name. It is for
// the router.
func (s *Server) Pending(name string) bool { return s.proofs.pending(name) }

// Proven is told by the router that a player logged in as name and is, as
// Mojang vouches, the account of uuid. If a check of that account is open, it
// gives them a code to enter in the panel; it says what to tell the player.
func (s *Server) Proven(_ context.Context, name, uuid string) string {
	code, found := s.proofs.give(name, uuid)
	switch {
	case code != "":
		return "Your code is " + code + ". Enter it in the panel."
	case found:
		return "You are signed in as another account, not " + name + "."
	default:
		return "No check is open for " + name + ". Click Verify in the panel first."
	}
}
