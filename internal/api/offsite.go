package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// offsite keeps what copying snapshots offsite needs to remember between
// looks.
type offsite struct {
	mu sync.Mutex
	// started is when this process began. Whatever changed while the panel
	// was not running was not seen, so every organization is due one sync
	// after a start.
	started time.Time
	// running holds the organizations being synced right now.
	running map[string]bool
	// busy is how many of those the panel started by itself.
	busy int
}

const (
	// offsiteSettle is how long an organization's snapshots have to have been
	// left alone before they are copied, so that a run of them is one sync.
	offsiteSettle = 2 * time.Minute
	// offsiteRetry is how long a failed sync waits before it is tried again
	// on its own.
	offsiteRetry = time.Hour
	// offsiteTimeout bounds one sync. The first one of a large organization
	// uploads everything it has.
	offsiteTimeout = 12 * time.Hour
)

var errHasSnapshots = &Error{Status: http.StatusConflict, Code: "has_snapshots", Message: "This organization has snapshots here already. Snapshots are only brought back into an organization that has none."}

var errNoOffsite = &Error{Status: http.StatusServiceUnavailable, Code: "no_offsite", Message: "No offsite copy is set up on this install."}

// claim marks an organization as being synced. ok is false if it is already.
func (o *offsite) claim(org string) (release func(), ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.running == nil {
		o.running = map[string]bool{}
	}
	if o.running[org] {
		return nil, false
	}
	o.running[org] = true
	return func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		delete(o.running, org)
	}, true
}

// offsiteAtOnce is how many organizations the panel copies at a time on its
// own. A copy asked for by hand does not count against it.
const offsiteAtOnce = 3

// slot takes one of the places for a sync the panel starts by itself. ok is
// false when they are all taken.
func (o *offsite) slot() (release func(), ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.busy >= offsiteAtOnce {
		return nil, false
	}
	o.busy++
	return func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.busy--
	}, true
}

func (o *offsite) isRunning(org string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running[org]
}

func (o *offsite) since() time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.started.IsZero() {
		o.started = time.Now()
	}
	return o.started
}

// changed is when an organization's snapshots last changed as far as the
// offsite copy is concerned: since this process started at the earliest.
func (s *Server) offsiteChanged(org string) time.Time {
	at := s.offsite.since()
	if c := s.Backups.Changed(org); c.After(at) {
		at = c
	}
	return at
}

// offsitePending reports whether an organization has something here that
// has not been copied offsite yet, as far as can be told without asking.
//
// After a failure it is taken to have: what the attempt did and did not get
// done is not known, and trying again is what finds out.
func (s *Server) offsitePending(org string, st backup.OffsiteStatus) bool {
	return st.Error != "" || st.LastSuccess == nil || st.LastSuccess.Before(s.offsiteChanged(org))
}

// RunOffsite keeps the offsite copy of every organization current until ctx
// ends. It looks once a minute and syncs the ones whose snapshots have
// changed and then been left alone for a moment.
func (s *Server) RunOffsite(ctx context.Context) {
	if !s.Backups.HasOffsite() || !s.Backups.Available() {
		return
	}
	s.offsite.since()
	for {
		s.offsiteTick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// offsiteTick starts a sync of every organization that is due one. Each runs
// on its own, a few at a time: one organization's first copy can take
// hours, and the others' new snapshots are not to wait behind it.
func (s *Server) offsiteTick(ctx context.Context, now time.Time) {
	for _, org := range s.Backups.Organizations() {
		if ctx.Err() != nil {
			return
		}
		if s.offsite.isRunning(org) || !s.offsiteDue(org, now) {
			continue
		}
		done, ok := s.offsite.slot()
		if !ok {
			// Looked at again in a minute.
			return
		}
		go func() {
			defer done()
			_, _ = s.syncOffsite(ctx, org)
		}()
	}
}

// offsiteDue reports whether an organization should be synced now.
func (s *Server) offsiteDue(org string, now time.Time) bool {
	st := s.Backups.OffsiteStatus(org)
	if !s.offsitePending(org, st) {
		return false
	}
	if now.Sub(s.Backups.Changed(org)) < offsiteSettle {
		return false
	}
	// A failure is tried again after a while, or as soon as there is
	// something new to copy.
	if st.Error != "" && st.LastAttempt != nil && now.Sub(*st.LastAttempt) < offsiteRetry && !st.LastAttempt.Before(s.offsiteChanged(org)) {
		return false
	}
	return true
}

// offsiteDone is sent when a sync has ended, so that pages showing how the
// copy stands ask again.
type offsiteDone struct {
	Org   string `json:"org"`
	Error string `json:"error,omitempty"`
}

// syncOffsite brings one organization's offsite copy up to date. It is not
// started while another sync of the same organization is going.
func (s *Server) syncOffsite(ctx context.Context, org string) (backup.OffsiteResult, error) {
	release, ok := s.offsite.claim(org)
	if !ok {
		return backup.OffsiteResult{}, &Error{Status: http.StatusConflict, Code: "busy", Message: "This organization's snapshots are being copied offsite right now."}
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, offsiteTimeout)
	defer cancel()
	res, err := s.Backups.SyncOffsite(ctx, org)
	done := offsiteDone{Org: org}
	if err != nil {
		s.Log.Error("copying snapshots offsite failed", "org", org, "error", err)
		done.Error = s.Backups.OffsiteStatus(org).Error
	} else if res.Copied > 0 || res.Removed > 0 {
		s.Log.Info("copied snapshots offsite", "org", org, "copied", res.Copied, "removed", res.Removed, "held", res.Snapshots)
	}
	s.Events.Publish(Event{Type: "offsite.done", Namespace: s.Namespace(org), Data: done})
	return res, err
}

// offsiteView is how an organization's offsite copy stands.
type offsiteView struct {
	// Configured is false when the install has no offsite copy at all, and
	// everything else is then left out.
	Configured bool `json:"configured"`
	backup.OffsiteStatus
	// Pending is set when something here has yet to be copied.
	Pending bool `json:"pending"`
	// Running is set while a sync is going.
	Running bool `json:"running"`
}

func (s *Server) viewOffsite(org string) offsiteView {
	if !s.Backups.HasOffsite() {
		return offsiteView{}
	}
	st := s.Backups.OffsiteStatus(org)
	v := offsiteView{Configured: true, OffsiteStatus: st, Running: s.offsite.isRunning(org)}
	// With no repository here there is nothing that could be waiting.
	v.Pending = s.Backups.HasRepository(org) && s.offsitePending(org, st)
	return v
}

func (s *Server) getOffsite(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.viewOffsite(a.Org.Slug))
	return nil
}

// runOffsite copies the organization's snapshots offsite now. It answers at
// once; with ?wait=true it answers when the sync is over, with what it did.
func (s *Server) runOffsite(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	if !s.Backups.HasOffsite() {
		return errNoOffsite
	}
	if s.offsite.isRunning(a.Org.Slug) {
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "This organization's snapshots are being copied offsite right now."}
	}
	s.audit(r.Context(), u, a.Org, "backups.offsite", a.Org.Slug, "")
	ctx := context.WithoutCancel(r.Context())
	if r.URL.Query().Get("wait") == "true" {
		res, err := s.syncOffsite(ctx, a.Org.Slug)
		if err != nil {
			return offsiteFailed(err)
		}
		writeJSON(w, http.StatusOK, res)
		return nil
	}
	go func() { _, _ = s.syncOffsite(ctx, a.Org.Slug) }()
	writeJSON(w, http.StatusAccepted, s.viewOffsite(a.Org.Slug))
	return nil
}

func offsiteFailed(err error) error {
	var known *Error
	if errors.As(err, &known) {
		return known
	}
	return &Error{Status: http.StatusBadGateway, Code: "offsite_failed", Message: "Copying offsite failed: " + err.Error()}
}

// offsiteSnapshots lists what could be brought back from the offsite copy
// of an organization: what it holds, less what was removed here on purpose
// and is only waiting to be removed there. Asking means
// reaching the far end, so it is not part of the status.
//
// It is for those who run the install. An organization made under the name
// of one that was deleted has that one's snapshots offsite, and they are not
// its members' to see or take.
func (s *Server) offsiteSnapshots(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if !u.IsAdmin {
		return errForbidden
	}
	if !s.Backups.HasOffsite() {
		return errNoOffsite
	}
	list, err := s.Backups.Recoverable(r.Context(), a.Org.Slug)
	if err != nil {
		return offsiteFailed(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
	return nil
}

// fetchOffsite brings an organization's snapshots back from the offsite
// copy, after the panel's own storage was lost. It only does so into an
// organization that has none: anything else would bring back snapshots that
// were deleted here on purpose and not yet removed there.
func (s *Server) fetchOffsite(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if !u.IsAdmin {
		return errForbidden
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	if !s.Backups.HasOffsite() {
		return errNoOffsite
	}
	if n, known := s.Backups.Holds(r.Context(), a.Org.Slug); !known || n > 0 {
		return errHasSnapshots
	}
	release, ok := s.offsite.claim(a.Org.Slug)
	if !ok {
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "This organization's snapshots are being copied right now."}
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), offsiteTimeout)
	defer cancel()
	fetched, err := s.Backups.FetchOffsite(ctx, a.Org.Slug)
	// Found out with the repository held: one may have been taken since the
	// look above.
	if errors.Is(err, backup.ErrNotEmpty) {
		return errHasSnapshots
	}
	if err != nil {
		return offsiteFailed(err)
	}
	s.audit(r.Context(), u, a.Org, "backups.fetch", a.Org.Slug, "")
	s.Events.Publish(Event{Type: "offsite.done", Namespace: a.Namespace, Data: offsiteDone{Org: a.Org.Slug}})
	writeJSON(w, http.StatusOK, map[string]any{"fetched": fetched})
	return nil
}
