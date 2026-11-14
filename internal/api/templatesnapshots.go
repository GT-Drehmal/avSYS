package api

import (
	"context"
	"errors"
	"sync"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// The snapshots templates and the library are made of, those taken as
// templates are (a template's versions and its own world, and each world in
// the library), are noted in the store as the panel makes and removes them,
// with all the pages show of them. Templates and worlds are listed from
// that, and need no list of snapshots from restic, which takes up to a
// second each time the repository has changed.
//
// What is noted is kept to what the repository has: an organization's are
// read from its repository the first time they are asked for, again each
// time the panel starts, and after snapshots are brought back from offsite;
// and one removed from the repository other than by the panel is not
// listed, as the repository's folder of snapshots says in a moment.

// noting holds, by organization ID, a lock taken to note or forget its
// template snapshots, and held across reading them all from the repository:
// what is read there is never noted over one made or removed meanwhile.
var noting sync.Map

func notingLock(orgID string) *sync.Mutex {
	l, _ := noting.LoadOrStore(orgID, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func notedOf(snap *backup.Snapshot) store.TemplateSnapshot {
	return store.TemplateSnapshot{ID: snap.ID, Host: snap.Server, Time: snap.Time, By: snap.By, Version: snap.Version, Loader: snap.Loader,
		Size: snap.Size, Added: snap.Added, Files: snap.Files, Lineage: snap.Lineage}
}

func snapshotOfNoted(t *store.TemplateSnapshot) backup.Snapshot {
	return backup.Snapshot{ID: t.ID, Time: t.Time, Server: t.Host, Trigger: backup.TriggerTemplate, By: t.By, Version: t.Version, Loader: t.Loader,
		Size: t.Size, Added: t.Added, Files: t.Files, Lineage: t.Lineage}
}

// templateSnapshots are an organization's template snapshots, newest first,
// as the store has them: none where there are none to have.
func (s *Server) templateSnapshots(ctx context.Context, org *store.Org) ([]backup.Snapshot, error) {
	if !s.Backups.Available() {
		return nil, nil
	}
	read, err := s.Store.TemplateSnapshotsRead(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	if !read {
		if _, _, err := s.readTemplateSnapshots(ctx, org); err != nil {
			return nil, snapshotError(err)
		}
	}
	noted, err := s.Store.TemplateSnapshots(ctx, org.ID)
	if err != nil {
		return nil, err
	}
	there, known := s.Backups.Present(org.Slug)
	out := make([]backup.Snapshot, 0, len(noted))
	for i := range noted {
		if known && !there[noted[i].ID] {
			continue
		}
		out = append(out, snapshotOfNoted(&noted[i]))
	}
	return out, nil
}

// repoSnapshots are all of an organization's snapshots, as its repository
// lists them, or none where there are none to have. What removes a
// template's or a world's snapshots finds them here, those the store may not
// have noted among them.
func (s *Server) repoSnapshots(ctx context.Context, org string) ([]backup.Snapshot, error) {
	if !s.Backups.Available() {
		return nil, nil
	}
	list, err := s.Backups.List(ctx, org)
	return list, snapshotErrorOrNil(err)
}

// noteTemplateSnapshot notes a template snapshot just made.
func (s *Server) noteTemplateSnapshot(ctx context.Context, org *store.Org, snap *backup.Snapshot) error {
	l := notingLock(org.ID)
	l.Lock()
	defer l.Unlock()
	return s.Store.NoteTemplateSnapshot(ctx, org.ID, notedOf(snap))
}

// forgetTemplateSnapshots forgets template snapshots just removed. One left
// noted is not listed, its snapshot being gone, and is forgotten when the
// panel next starts.
func (s *Server) forgetTemplateSnapshots(ctx context.Context, org *store.Org, ids ...string) {
	l := notingLock(org.ID)
	l.Lock()
	defer l.Unlock()
	if err := s.Store.ForgetTemplateSnapshots(ctx, org.ID, ids...); err != nil {
		s.Log.Warn("could not forget template snapshots that were removed", "org", org.Slug, "error", err)
	}
}

// forgetTemplateSnapshotsOf forgets every template snapshot noted under
// these hosts, once they are removed.
func (s *Server) forgetTemplateSnapshotsOf(ctx context.Context, org *store.Org, hosts ...string) {
	l := notingLock(org.ID)
	l.Lock()
	defer l.Unlock()
	if err := s.Store.ForgetTemplateSnapshotsOf(ctx, org.ID, hosts...); err != nil {
		s.Log.Warn("could not forget template snapshots that were removed", "org", org.Slug, "error", err)
	}
}

// readTemplateSnapshots notes an organization's template snapshots as its
// repository lists them now, in place of what was noted.
func (s *Server) readTemplateSnapshots(ctx context.Context, org *store.Org) (added, removed int, err error) {
	l := notingLock(org.ID)
	l.Lock()
	defer l.Unlock()
	list, err := s.Backups.List(ctx, org.Slug)
	if err != nil {
		return 0, 0, err
	}
	var all []store.TemplateSnapshot
	for i := range list {
		if isTemplate(&list[i]) {
			all = append(all, notedOf(&list[i]))
		}
	}
	return s.Store.SetTemplateSnapshots(ctx, org.ID, all)
}

// ReadTemplateSnapshots reads every organization's template snapshots from
// its repository, as the panel starts: those of a panel from before they
// were noted, and any made or removed while it was not running.
func (s *Server) ReadTemplateSnapshots(ctx context.Context) {
	if !s.Backups.Available() {
		return
	}
	orgs, added, removed := 0, 0, 0
	for _, slug := range s.Backups.Organizations() {
		if ctx.Err() != nil {
			return
		}
		org, err := s.Store.OrgBySlug(ctx, slug)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			s.Log.Warn("could not read an organization's template snapshots", "org", slug, "error", err)
			continue
		}
		a, r, err := s.readTemplateSnapshots(ctx, org)
		if err != nil {
			s.Log.Warn("could not read an organization's template snapshots", "org", slug, "error", err)
			continue
		}
		if a > 0 || r > 0 {
			s.Log.Info("noted an organization's template snapshots", "org", slug, "added", a, "removed", r)
		}
		orgs, added, removed = orgs+1, added+a, removed+r
	}
	s.Log.Info("read the template snapshots", "organizations", orgs, "added", added, "removed", removed)
}
