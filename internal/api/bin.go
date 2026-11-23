package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Recently deleted: a snapshot someone deletes is kept for a while, hidden,
// before it is removed for good, so that one click, mistaken or not, is not
// the end of it. How long is the panel's administrator's to say, for the
// whole panel; 0 removes snapshots at once, as before. Only the panel's
// administrator can remove them sooner. What the schedule lets go of is
// removed at once, as ever: it does not go through the bin.

const (
	binDaysKey     = "snapshots.bin-days"
	defaultBinDays = 7
	maxBinDays     = 90
)

// binDays is how many days a deleted snapshot is kept.
func (s *Server) binDays(ctx context.Context) int {
	v, err := s.Store.Setting(ctx, binDaysKey)
	if err != nil {
		return defaultBinDays
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > maxBinDays {
		return defaultBinDays
	}
	return n
}

// setBinDays sets how many days a deleted snapshot is kept. What is in the
// bin already is kept as long as the new number says, from when it was
// deleted.
func (s *Server) setBinDays(ctx context.Context, u *store.User, days int) error {
	if days < 0 || days > maxBinDays {
		return errBadRequest("Deleted snapshots are kept for 0 to %d days.", maxBinDays)
	}
	if err := s.Store.SetSetting(ctx, binDaysKey, strconv.Itoa(days)); err != nil {
		return err
	}
	s.audit(ctx, u, nil, "panel.bin-days", strconv.Itoa(days), "")
	return nil
}

// binUntil is when a snapshot deleted at is removed for good.
func binUntil(at time.Time, days int) time.Time { return at.Add(time.Duration(days) * 24 * time.Hour) }

var errOnlyPanelAdmin = &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Only the panel's administrator can remove deleted snapshots early."}

// binView is a snapshot in the bin as it is listed.
type binView struct {
	snapshotView
	DeletedAt time.Time `json:"deletedAt"`
	DeletedBy string    `json:"deletedBy,omitempty"`
	// Until is when it is removed for good.
	Until time.Time `json:"until"`
}

type binList struct {
	// Available is false when the panel cannot run restic at all.
	Available bool `json:"available"`
	// Days is how long a deleted snapshot is kept; 0 for not at all.
	Days int `json:"days"`
	// CanEmpty is whether the one asking may remove them early.
	CanEmpty  bool      `json:"canEmpty"`
	Snapshots []binView `json:"snapshots"`
}

// binOf is what is in an organization's bin, with names.
func (s *Server) binOf(ctx context.Context, a *access) ([]binView, error) {
	list, err := s.Backups.BinList(ctx, a.Org.Slug)
	if err != nil {
		return nil, err
	}
	days := s.binDays(ctx)
	snaps := make([]backup.Snapshot, len(list))
	for i := range list {
		snaps[i] = list[i].Snapshot
	}
	named := s.named(ctx, a, snaps)
	out := make([]binView, len(list))
	for i, b := range list {
		out[i] = binView{snapshotView: named[i], DeletedAt: b.DeletedAt, DeletedBy: b.DeletedBy, Until: binUntil(b.DeletedAt, days)}
	}
	return out, nil
}

// listBin lists an organization's recently deleted snapshots.
func (s *Server) listBin(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	out := binList{Available: s.Backups.Available(), Days: s.binDays(r.Context()), CanEmpty: u.IsAdmin, Snapshots: []binView{}}
	if out.Available {
		if out.Snapshots, err = s.binOf(r.Context(), a); err != nil {
			return snapshotError(err)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// binSnapshots puts snapshots in the bin, or with no days to keep them for
// removes them at once, and says for how many days they are kept. They have
// been checked as not in use.
func (s *Server) binSnapshots(ctx context.Context, a *access, u *store.User, snaps []*backup.Snapshot) (days int, err error) {
	ids := make([]string, len(snaps))
	servers := []string{}
	seen := map[string]bool{}
	for i, snap := range snaps {
		ids[i] = snap.ID
		if !seen[snap.Server] {
			seen[snap.Server] = true
			servers = append(servers, snap.Server)
		}
	}
	days = s.binDays(ctx)
	if days > 0 {
		// Its name and its server's notes stay with it, for when it is put
		// back.
		if _, err := s.Backups.Bin(ctx, a.Org.Slug, ids, u.Username, time.Now()); err != nil {
			return 0, snapshotError(err)
		}
	} else {
		if err := s.Backups.DeleteMany(ctx, a.Org.Slug, ids); err != nil {
			return 0, snapshotError(err)
		}
		if err := s.Store.DeleteSnapshotNames(ctx, a.Org.ID, ids...); err != nil {
			s.Log.Warn("could not forget the names of deleted snapshots", "org", a.Org.Slug, "error", err)
		}
		// A deleted server with no snapshots left cannot be brought back.
		defer s.forgetNotesOfGone(ctx, a, servers...)
	}
	for _, snap := range snaps {
		s.audit(ctx, u, a.Org, "snapshot.delete", snap.Server, snap.ID)
	}
	for _, server := range servers {
		s.Events.Publish(Event{Type: "operation.done", Namespace: a.Namespace, Data: operationDone{Kind: "delete", Org: a.Org.Slug, Server: server}})
	}
	return days, nil
}

// putBackSnapshot takes a snapshot out of the bin, into the lists again.
// Putting one back is for those who can restore one.
func (s *Server) putBackSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	back, err := s.Backups.PutBack(context.WithoutCancel(r.Context()), a.Org.Slug, []string{r.PathValue("id")})
	if err != nil {
		return snapshotError(err)
	}
	snap := back[0]
	s.audit(r.Context(), u, a.Org, "snapshot.put-back", snap.Server, snap.ID)
	s.Events.Publish(Event{Type: "operation.done", Namespace: a.Namespace, Data: operationDone{Kind: "delete", Org: a.Org.Slug, Server: snap.Server}})
	writeJSON(w, http.StatusOK, s.named(r.Context(), a, []backup.Snapshot{snap.Snapshot})[0])
	return nil
}

// emptyBinSnapshot removes one snapshot in the bin for good, before its
// time.
func (s *Server) emptyBinSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
	return s.emptyBin(w, r, u, []string{r.PathValue("id")})
}

// emptyBinAll removes every snapshot in an organization's bin for good.
func (s *Server) emptyBinAll(w http.ResponseWriter, r *http.Request, u *store.User) error {
	return s.emptyBin(w, r, u, nil)
}

func (s *Server) emptyBin(w http.ResponseWriter, r *http.Request, u *store.User, ids []string) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if !u.IsAdmin {
		return errOnlyPanelAdmin
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	ctx := context.WithoutCancel(r.Context())
	emptied, err := s.Backups.EmptyBin(ctx, a.Org.Slug, ids, nil)
	if err != nil {
		return snapshotError(err)
	}
	s.emptied(ctx, a, u, emptied, "snapshot.empty")
	writeJSON(w, http.StatusOK, map[string]int{"removed": len(emptied)})
	return nil
}

// emptied finishes with snapshots removed from the bin for good: their names
// go, so do the notes of a deleted server that has nothing left to be
// brought back from, and it is noted who did it, or that the panel did.
func (s *Server) emptied(ctx context.Context, a *access, u *store.User, gone []backup.Binned, action string) {
	if len(gone) == 0 {
		return
	}
	ids := make([]string, len(gone))
	servers := []string{}
	seen := map[string]bool{}
	for i, b := range gone {
		ids[i] = b.ID
		if !seen[b.Server] {
			seen[b.Server] = true
			servers = append(servers, b.Server)
		}
	}
	if err := s.Store.DeleteSnapshotNames(ctx, a.Org.ID, ids...); err != nil {
		s.Log.Warn("could not forget the names of snapshots removed for good", "org", a.Org.Slug, "error", err)
	}
	for _, b := range gone {
		s.audit(ctx, u, a.Org, action, b.Server, b.ID)
	}
	for _, server := range servers {
		s.Events.Publish(Event{Type: "operation.done", Namespace: a.Namespace, Data: operationDone{Kind: "delete", Org: a.Org.Slug, Server: server, Scheduled: u == nil}})
	}
	s.forgetNotesOfGone(ctx, a, servers...)
}

// RunBin removes deleted snapshots for good once they have been kept as
// long as they are to be. It looks once a minute, until ctx ends.
func (s *Server) RunBin(ctx context.Context) {
	for {
		s.emptyExpired(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// emptyExpired removes, in every organization, the deleted snapshots whose
// time is up by now.
func (s *Server) emptyExpired(ctx context.Context, now time.Time) {
	if !s.Backups.Available() {
		return
	}
	days := s.binDays(ctx)
	due := func(b backup.Binned) bool { return !binUntil(b.DeletedAt, days).After(now) }
	for _, slug := range s.Backups.Organizations() {
		if ctx.Err() != nil {
			return
		}
		// Read from the file alone: the repository is not held, or so much
		// as listed, for an organization with nothing due.
		if !s.Backups.BinDue(slug, func(at time.Time) bool { return !binUntil(at, days).After(now) }) {
			continue
		}
		org, err := s.Store.OrgBySlug(ctx, slug)
		if err != nil {
			continue
		}
		a := &access{Org: org, Role: store.RoleOwner, Namespace: s.Namespace(slug)}
		gone, err := s.Backups.EmptyBin(context.WithoutCancel(ctx), slug, nil, due)
		if err != nil {
			s.Log.Error("could not remove deleted snapshots whose time was up; trying again in a minute", "org", slug, "error", err)
			continue
		}
		s.emptied(ctx, a, nil, gone, "snapshot.expire")
	}
}

// serverSnapshotsIn counts an organization's snapshots of servers, not of
// templates or worlds: those listed and those in the bin.
func (s *Server) serverSnapshotsIn(ctx context.Context, slug string) (listed, binned int, err error) {
	list, err := s.Backups.List(ctx, slug)
	if err != nil {
		return 0, 0, err
	}
	for i := range list {
		if !isTemplate(&list[i]) {
			listed++
		}
	}
	bin, err := s.Backups.BinList(ctx, slug)
	if err != nil {
		return 0, 0, err
	}
	return listed, len(bin), nil
}

// orgKeepsSnapshots refuses to delete an organization whose servers'
// snapshots would go with it, while deleted snapshots are kept: deleting the
// organization would remove them at once, bin and all. They are deleted
// first, and wait out their time in the bin. Only the panel's administrator
// may delete such an organization at once.
func (s *Server) orgKeepsSnapshots(ctx context.Context, u *store.User, slug string) error {
	days := s.binDays(ctx)
	if u.IsAdmin || days == 0 {
		return nil
	}
	// Without restic they cannot be told apart, but would go all the same:
	// only an organization with nothing at all here is deleted.
	if !s.Backups.Available() {
		if n, known := s.Backups.Holds(ctx, slug); !known || n > 0 {
			return &Error{Status: http.StatusConflict, Code: "has_snapshots", Message: "This organization's snapshots could not be counted, so it was not deleted. Only the panel's administrator can delete it now."}
		}
		return nil
	}
	listed, binned, err := s.serverSnapshotsIn(ctx, slug)
	if err != nil {
		return &Error{Status: http.StatusConflict, Code: "has_snapshots", Message: "This organization's snapshots could not be counted, so it was not deleted. Only the panel's administrator can delete it now."}
	}
	switch {
	case listed > 0:
		return &Error{Status: http.StatusConflict, Code: "has_snapshots", Message: fmt.Sprintf(
			"Delete this organization's snapshots first, on its Backups page. Deleted snapshots are kept for %d days; the organization can be deleted once they are gone.", days)}
	case binned > 0:
		return &Error{Status: http.StatusConflict, Code: "snapshots_kept", Message: fmt.Sprintf(
			"This organization's deleted snapshots are kept for %d days. It can be deleted once they are gone, or by the panel's administrator now.", days)}
	}
	return nil
}
