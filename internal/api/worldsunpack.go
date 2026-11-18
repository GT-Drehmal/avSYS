package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// The library kept each world as an archive before: one uploaded as it was
// sent, one saved as a tar of its world's folders. As the panel starts, each
// world still kept that way is kept as its own files instead (see
// worldunpack.go), one at a time, under the same ID, so that the servers
// that say they came from it, and putting it back in them, go on as they
// were. Until then, and for one that cannot be, it is used as it is.
//
// The archive is unpacked as an upload is, and the world kept under a host
// of its own (unpackingHost), checked against what was unpacked. Then, with
// the world held, as one being saved is, the snapshot is copied under the
// world's own host, where it is the newest and so the one used, and the
// archive is removed. A world whose archive could not be unpacked keeps it,
// and says why in the library; it is tried again the next time the panel
// starts.
//
// Cut short anywhere, what was left is cleared up the next time: a folder
// it was unpacked in, and the snapshot kept under the host of its own. One
// cut short once the copy was made is done by then, and its archive goes.

// worldsUnpacking holds, by organization ID and world ID, the worlds kept as
// an archive that are being kept as their own files now.
var worldsUnpacking sync.Map

func worldUnpacking(orgID, id string) bool {
	_, ok := worldsUnpacking.Load(orgID + "/" + id)
	return ok
}

// unpackingHost is what a world is kept under while it is checked, before
// it takes the place of its archive.
func unpackingHost(id string) string { return libraryHost(id) + "-unpacking" }

// processStart is when this panel started: work folders from before are
// left by one that stopped.
var processStart = time.Now()

// UnpackLibrary keeps the worlds of every organization's library still kept
// as an archive as their own files, as the panel starts.
func (s *Server) UnpackLibrary(ctx context.Context) {
	if !s.Backups.Available() {
		return
	}
	s.sweepWorldWork()
	for _, slug := range s.Backups.Organizations() {
		if ctx.Err() != nil {
			return
		}
		org, err := s.Store.OrgBySlug(ctx, slug)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			s.Log.Warn("could not read an organization to unpack its library", "org", slug, "error", err)
			continue
		}
		s.unpackLibrary(ctx, org)
	}
}

// sweepWorldWork removes the folders worlds were unpacked in by a panel
// that stopped before it was done with them.
func (s *Server) sweepWorldWork() {
	root := filepath.Dir(s.Backups.WorkDir("-"))
	orgs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, org := range orgs {
		dir := filepath.Join(root, org.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && fi.ModTime().Before(processStart) {
				if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
					s.Log.Warn("could not remove a world left unpacked", "dir", filepath.Join(dir, e.Name()), "error", err)
				}
			}
		}
	}
}

// unpackLibrary keeps an organization's worlds kept as an archive as their
// own files, one at a time.
func (s *Server) unpackLibrary(ctx context.Context, org *store.Org) {
	worlds, err := s.Store.Worlds(ctx, org.ID)
	if err != nil {
		s.Log.Warn("could not list an organization's worlds to unpack them", "org", org.Slug, "error", err)
		return
	}
	for i := range worlds {
		if ctx.Err() != nil {
			return
		}
		// Read again for each: unpacking one changes what is there.
		list, err := s.Backups.List(ctx, org.Slug)
		if err != nil {
			s.Log.Warn("could not list an organization's snapshots to unpack its worlds", "org", org.Slug, "error", err)
			return
		}
		s.unpackLibraryWorld(ctx, org, &worlds[i], list)
	}
}

// unpackLibraryWorld keeps one world as its own files, if it is kept as an
// archive, and clears up what one cut short left of it.
func (s *Server) unpackLibraryWorld(ctx context.Context, org *store.Org, w *store.World, list []backup.Snapshot) {
	a := &access{Org: org}
	// Newest first, as the repository lists them.
	var mine, left []backup.Snapshot
	for _, snap := range list {
		if !isTemplate(&snap) {
			continue
		}
		switch snap.Server {
		case libraryHost(w.ID):
			mine = append(mine, snap)
		case unpackingHost(w.ID):
			left = append(left, snap)
		}
	}
	if len(left) > 0 && !worldBusy(a, w.ID) {
		s.dropSnapshots(ctx, org, left, "could not remove a world left half unpacked")
	}
	if len(mine) == 0 || w.Failed != "" {
		return
	}
	if mine[0].Form == backup.FormFolders {
		// Kept as its own files, by one cut short once it had been: what is
		// known of it is set, and the archive goes.
		if len(mine) > 1 {
			release, err := claimWorld(a, w.ID)
			if err != nil {
				return
			}
			err = s.Store.SetWorldUnpacked(ctx, org.ID, w.ID, keptRoot(w, &mine[0]), mine[0].Size)
			release()
			if err != nil {
				s.Log.Warn("could not note a world as unpacked", "world", w.ID, "error", err)
				return
			}
			s.dropSnapshots(ctx, org, mine[1:], "could not remove a world's archive once it was unpacked")
		}
		return
	}
	// One cut short by the panel stopping is tried again as it starts, and
	// is not said to have failed.
	if err := s.unpackArchived(ctx, org, w, &mine[0]); err != nil && ctx.Err() == nil {
		s.Log.Warn("a world of the library could not be unpacked; it is kept as it was", "org", org.Slug, "world", w.ID, "name", w.Name, "error", err)
		if ferr := s.Store.SetWorldUnpackFailed(context.WithoutCancel(ctx), org.ID, w.ID, sentence(plainly(err))); ferr != nil && !errors.Is(ferr, store.ErrNotFound) {
			s.Log.Warn("could not say why a world was not unpacked", "world", w.ID, "error", ferr)
		}
	}
}

// dropSnapshots removes snapshots of the library, saying so if it cannot.
func (s *Server) dropSnapshots(ctx context.Context, org *store.Org, snaps []backup.Snapshot, why string) {
	ids := make([]string, len(snaps))
	for i := range snaps {
		ids[i] = snaps[i].ID
	}
	if err := s.Backups.DeleteMany(context.WithoutCancel(ctx), org.Slug, ids); err != nil {
		s.Log.Warn(why, "org", org.Slug, "snapshots", ids, "error", err)
		return
	}
	s.forgetTemplateSnapshots(context.WithoutCancel(ctx), org, ids...)
}

// unpackArchived keeps a world kept as the archive in old as its own files
// instead, and removes the archive once they are in its place.
func (s *Server) unpackArchived(ctx context.Context, org *store.Org, w *store.World, old *backup.Snapshot) error {
	a := &access{Org: org}
	if worldBusy(a, w.ID) {
		return nil
	}
	key := org.ID + "/" + w.ID
	worldsUnpacking.Store(key, true)
	defer worldsUnpacking.Delete(key)
	began := time.Now()
	s.Log.Info("unpacking a world of the library", "org", org.Slug, "world", w.ID, "name", w.Name, "archive", old.Size)

	dir, done, err := s.worldWork(org.Slug)
	if err != nil {
		return err
	}
	defer done()
	if free, err := freeSpace(dir); err != nil {
		return err
	} else if free < old.Size+worldRoomReserve {
		return errNoRoomFor(free)
	}
	file := filepath.Join(dir, "archive")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = s.Backups.Stream(ctx, org.Slug, old.ID, "/"+ownWorldFile, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	pending, unpacked, err := s.keepUnpacked(ctx, org.Slug, dir, file, w.Root, unpackingHost(w.ID), old.By, func() { _ = os.Remove(file) })
	if err != nil {
		return err
	}
	drop := func() {
		s.dropSnapshots(ctx, org, []backup.Snapshot{*pending}, "could not remove a world unpacked for nothing")
	}

	// Put in the archive's place with the world held, as one being saved or
	// deleted is, and as it still is: a minute is waited for one being
	// given a pack, say, to be done.
	var release func()
	for try := 0; ; try++ {
		if release, err = claimWorld(a, w.ID); err == nil {
			break
		}
		if try == 12 || ctx.Err() != nil {
			drop()
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	if _, err := s.Store.World(ctx, org.ID, w.ID); errors.Is(err, store.ErrNotFound) {
		drop()
		return nil
	} else if err != nil {
		drop()
		return err
	}
	if now, err := s.Backups.List(ctx, org.Slug); err != nil {
		drop()
		return err
	} else if snaps := librarySnapshots(now); snaps[w.ID].ID != old.ID {
		// Changed meanwhile: the next time, it is looked at again.
		drop()
		return nil
	}
	kept, err := s.Backups.CopyAs(ctx, org.Slug, pending.ID, libraryHost(w.ID), old.By)
	if err != nil {
		drop()
		return err
	}
	if err := s.noteTemplateSnapshot(ctx, org, kept); err != nil {
		s.Log.Warn("could not note a world's unpacked snapshot", "world", w.ID, "error", err)
	}
	if err := s.Store.SetWorldUnpacked(ctx, org.ID, w.ID, keptWorld, unpacked.Bytes); err != nil {
		s.Log.Warn("could not note a world as unpacked", "world", w.ID, "error", err)
	}
	// The archive is let go of with the world no longer held: its files are
	// the world now, and removing the archive, which prunes the repository,
	// can take a while. An import reading it meanwhile is waited for.
	release()
	held = false
	s.dropSnapshots(ctx, org, []backup.Snapshot{*pending, *old}, "could not remove a world's archive once it was unpacked")
	s.Log.Info("unpacked a world of the library", "org", org.Slug, "world", w.ID, "name", w.Name, "files", unpacked.Files, "size", unpacked.Bytes, "took", time.Since(began).Round(time.Second))
	return nil
}
