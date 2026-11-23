package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Notes on servers, and names and colours of snapshots, are kept by the
// panel for those who look after the servers: never in a server's settings,
// its files or anything the game shows. A template's or a library world's
// description is the same kind of thing, kept with it.

// maxNotes is the most a server's notes, or a template's or world's
// description, may be.
const maxNotes = 500

// checkNotes tidies a server's notes and checks them: a few lines of plain
// text.
func checkNotes(notes string) (string, error) {
	notes = strings.TrimSpace(notes)
	if len([]rune(notes)) > maxNotes {
		return "", errBadRequest("Notes can be at most %d characters.", maxNotes)
	}
	for _, c := range notes {
		if unicode.IsControl(c) && c != '\n' {
			return "", errBadRequest("Notes cannot have control characters.")
		}
	}
	return notes, nil
}

// notesOf is what is noted of a server, "" for nothing or when that cannot
// be read: notes are never worth failing an answer for.
func (s *Server) notesOf(ctx context.Context, mc *avsysv1.MinecraftServer) string {
	org, ok := s.orgOfNamespace(mc.Namespace)
	if !ok || s.Store == nil {
		return ""
	}
	notes, err := s.Store.ServerNotesIn(ctx, org, mc.Name)
	if err != nil {
		s.Log.Warn("could not read a server's notes", "namespace", mc.Namespace, "server", mc.Name, "error", err)
	}
	return notes
}

// keepNotes keeps a server's notes once it is made. Left out (nil), it has
// those given as the default: its original's, for a copy.
func (s *Server) keepNotes(ctx context.Context, a *access, name string, notes *string, otherwise string) {
	kept := otherwise
	if notes != nil {
		kept = *notes
	}
	if err := s.Store.SetServerNotes(ctx, a.Org.ID, name, kept); err != nil {
		s.Log.Error("could not keep a new server's notes", "org", a.Org.Slug, "server", name, "error", err)
	}
}

// forgetNotesOfGone forgets the notes of servers by these names that no
// longer exist and have no snapshots left to be brought back from. A server
// that is gone keeps its notes as long as it can be brought back, with its
// name.
func (s *Server) forgetNotesOfGone(ctx context.Context, a *access, names ...string) {
	var gone []string
	for _, name := range names {
		err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, &avsysv1.MinecraftServer{})
		if apierrors.IsNotFound(err) {
			gone = append(gone, name)
		} else if err != nil {
			return
		}
	}
	if len(gone) == 0 {
		return
	}
	if s.Backups.Available() {
		list, err := s.Backups.List(ctx, a.Org.Slug)
		if err != nil {
			return
		}
		// Those in the bin can be put back, and the server brought back
		// from them.
		binned, err := s.Backups.BinList(ctx, a.Org.Slug)
		if err != nil {
			return
		}
		for _, b := range binned {
			list = append(list, b.Snapshot)
		}
		for _, snap := range list {
			if !isTemplate(&snap) {
				gone = slices.DeleteFunc(gone, func(name string) bool { return name == snap.Server })
			}
		}
	}
	for _, name := range gone {
		if err := s.Store.DeleteServerNotes(ctx, a.Org.ID, name); err != nil {
			s.Log.Warn("could not forget a deleted server's notes", "org", a.Org.Slug, "server", name, "error", err)
		}
	}
}

// snapshotColors are the colours a snapshot can be marked with: the
// panel's accents, which each theme draws in its own way.
var snapshotColors = []string{"amethyst", "gold", "teal", "blue", "rose"}

// maxSnapshotName is the most a snapshot's name may be.
const maxSnapshotName = 60

// snapshotView is a snapshot as it is listed, with the name and colour it
// was given here.
type snapshotView struct {
	backup.Snapshot
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"`
}

// named gives snapshots their names. Without the names, they are listed
// without: a fault here does not hide the snapshots.
func (s *Server) named(ctx context.Context, a *access, list []backup.Snapshot) []snapshotView {
	names, err := s.Store.SnapshotNames(ctx, a.Org.ID)
	if err != nil {
		s.Log.Warn("could not read the names of snapshots", "org", a.Org.Slug, "error", err)
	}
	out := make([]snapshotView, len(list))
	for i, snap := range list {
		n := names[snap.ID]
		out[i] = snapshotView{Snapshot: snap, Name: n.Name, Color: n.Color}
	}
	return out
}

// nameSnapshot names a snapshot, gives it a colour, or both: what is left
// out stays as it is, and "" takes it away. A named snapshot is kept until
// someone deletes it, as one taken by hand is: the schedule passes it over.
func (s *Server) nameSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	var in struct {
		Name, Color *string
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Name == nil && in.Color == nil {
		return errBadRequest(`Nothing to change: send {"name": ..., "color": ...}.`)
	}
	var name, color string
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if len([]rune(name)) > maxSnapshotName {
			return errBadRequest("A snapshot's name can be at most %d characters.", maxSnapshotName)
		}
		for _, c := range name {
			if unicode.IsControl(c) {
				return errBadRequest("A snapshot's name is one line, with no control characters.")
			}
		}
	}
	if in.Color != nil {
		color = *in.Color
		if color != "" && !slices.Contains(snapshotColors, color) {
			return errBadRequest("A snapshot's colour is one of %s, or \"\" for none.", strings.Join(snapshotColors, ", "))
		}
	}
	// Named with the snapshot held, so that the schedule, which reads the
	// names with the repository held, either sees the name or has already
	// removed the snapshot, which is then not named.
	var snap *backup.Snapshot
	var n store.SnapshotName
	err = s.Backups.Held(context.WithoutCancel(r.Context()), a.Org.Slug, r.PathValue("id"), func(held *backup.Snapshot) error {
		snap = held
		if isTemplate(snap) {
			return errTemplateVersion
		}
		names, err := s.Store.SnapshotNames(r.Context(), a.Org.ID)
		if err != nil {
			return err
		}
		n = names[snap.ID]
		if in.Name != nil {
			n.Name = name
		}
		if in.Color != nil {
			n.Color = color
		}
		n.By = u.Username
		return s.Store.SetSnapshotName(r.Context(), a.Org.ID, snap.ID, n)
	})
	if errors.Is(err, backup.ErrNotFound) {
		return snapshotError(err)
	} else if err != nil {
		return err
	}
	detail := n.Name
	if n.Color != "" {
		detail = strings.TrimSpace(detail + " (" + n.Color + ")")
	}
	s.audit(r.Context(), u, a.Org, "snapshot.name", snap.Server, strings.TrimSpace(shortID(snap.ID)+" "+detail))
	writeJSON(w, http.StatusOK, snapshotView{Snapshot: *snap, Name: n.Name, Color: n.Color})
	return nil
}

// forgetGoneNames forgets the names of snapshots that are not in the
// repository's list, read at listed: removed some way other than through
// the panel, or the panel stopped before it forgot them itself.
func (s *Server) forgetGoneNames(ctx context.Context, a *access, list []backup.Snapshot, listed time.Time) {
	has := make(map[string]bool, len(list))
	for _, snap := range list {
		has[snap.ID] = true
	}
	if err := s.Store.ForgetSnapshotNamesBut(ctx, a.Org.ID, has, listed); err != nil {
		s.Log.Warn("could not forget the names of snapshots that are gone", "org", a.Org.Slug, "error", err)
	}
}
