package api

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A world in the library can name the resource pack it is played with, as an
// adventure map usually ships one: one from an address, or one uploaded to
// the panel. An uploaded one is kept as a snapshot of its own beside the
// world's archive, and so is the one a template's own world has. Each server
// given the world is given a copy of the pack, kept in its own files as any
// pack uploaded to it is.

// packKeptFile is what an uploaded pack is called in its snapshot: what the
// game calls a world's own pack in a single-player save.
const packKeptFile = "resources.zip"

// packHost is what the pack of a world kept under host is kept under.
func packHost(host string) string { return host + "-pack" }

// worldPackOf is the resource pack a world in the library names, or nil.
func worldPackOf(w *store.World) *avsysv1.ResourcePackSpec {
	return packOfJSON(w.Pack)
}

func packOfJSON(raw string) *avsysv1.ResourcePackSpec {
	if raw == "" {
		return nil
	}
	var rp avsysv1.ResourcePackSpec
	if json.Unmarshal([]byte(raw), &rp) != nil {
		return nil
	}
	return &rp
}

func packJSON(rp *avsysv1.ResourcePackSpec) string {
	if rp == nil {
		return ""
	}
	raw, _ := json.Marshal(rp)
	return string(raw)
}

// keptPackSnapshots are the snapshots of the packs kept under host's pack
// host, newest first: one, but for a moment while another takes its place.
func keptPackSnapshots(list []backup.Snapshot, host string) []backup.Snapshot {
	var out []backup.Snapshot
	for _, snap := range list {
		if snap.Trigger == backup.TriggerTemplate && snap.Server == packHost(host) {
			out = append(out, snap)
		}
	}
	return out
}

// keptPack is the snapshot the pack of what is kept under host is in.
func (s *Server) keptPack(ctx context.Context, org, host string) (*backup.Snapshot, error) {
	list, err := s.templateSnapshots(ctx, org)
	if err != nil {
		return nil, err
	}
	if kept := keptPackSnapshots(list, host); len(kept) > 0 {
		return &kept[0], nil
	}
	return nil, &Error{Status: http.StatusNotFound, Code: "no_pack", Message: "The resource pack kept with that world is missing. Upload it to the world again."}
}

// dropKeptPacks removes the packs kept under host's pack host but keep.
func (s *Server) dropKeptPacks(ctx context.Context, a *access, host, keep string) {
	list, err := s.templateSnapshots(ctx, a.Org.Slug)
	if err != nil {
		s.Log.Warn("could not list the resource packs kept with a world", "host", host, "error", err)
		return
	}
	var old []string
	for _, snap := range keptPackSnapshots(list, host) {
		if snap.ID != keep {
			old = append(old, snap.ID)
		}
	}
	if err := s.Backups.DeleteMany(ctx, a.Org.Slug, old); err != nil {
		s.Log.Warn("could not remove a resource pack kept with a world", "host", host, "error", err)
	}
}

// readSnapshotFile is one file of a snapshot, read out as it is wanted.
// done lets go of it, whether or not all of it was read.
func (s *Server) readSnapshotFile(ctx context.Context, org, id, file string) (body io.Reader, done func()) {
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(s.Backups.Stream(ctx, org, id, file, pw)) }()
	return pr, func() {
		pr.CloseWithError(io.ErrClosedPipe)
		cancel()
	}
}

// keepPack checks a resource pack read from src and keeps it under host's
// pack host, as uploaded under name. Its settings are left for the caller,
// and so are the packs kept there before.
func (s *Server) keepPack(ctx context.Context, a *access, host string, src io.Reader, name, by string) (*avsysv1.ResourcePackSpec, *backup.Snapshot, error) {
	if s.Config.PackDir == "" {
		return nil, nil, errPacksUnavailable
	}
	tmp, err := s.packTemp("upload-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	// The most any game version takes: which one it is given to is not
	// known yet, and one that takes less says so then.
	limit := packLimit("")
	size, sha, err := copyHashed(tmp, src, limit)
	if err != nil {
		return nil, nil, errBadRequest("The upload was incomplete.")
	}
	if size > limit {
		return nil, nil, errBadRequest("That pack is over %d MB, the most the game takes.", limit>>20)
	}
	if size == 0 {
		return nil, nil, errBadRequest("The upload was empty.")
	}
	formats, err := checkPack(tmp, size)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	ctx = context.WithoutCancel(ctx)
	kept, err := s.Backups.KeepFile(ctx, a.Org.Slug, packHost(host), packKeptFile, by, tmp)
	if err != nil {
		if kept != nil {
			if derr := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); derr != nil {
				s.Log.Error("could not remove a resource pack kept in part", "host", host, "snapshot", kept.ID, "error", derr)
			}
		}
		return nil, nil, snapshotError(err)
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" {
		name = packKeptFile
	}
	return &avsysv1.ResourcePackSpec{SHA1: sha, Uploaded: &avsysv1.UploadedPack{Name: name, Size: size, Time: metav1.Now()}, Formats: formats}, kept, nil
}

// packSource is the pack a request gives: with ?world= the one uploaded to
// a world in the library, with ?template= the one a template's own world
// has, read out of where it is kept; otherwise the body, named by ?name=.
func (s *Server) packSource(r *http.Request, a *access) (body io.Reader, name string, done func(), err error) {
	q := r.URL.Query()
	world, template := q.Get("world"), q.Get("template")
	if world == "" && template == "" {
		return r.Body, q.Get("name"), func() {}, nil
	}
	if world != "" && template != "" {
		return nil, "", nil, errBadRequest("A resource pack comes from a world or from a template, not both.")
	}
	var rp *avsysv1.ResourcePackSpec
	var host string
	if world != "" {
		w, _, err := s.readyWorld(r.Context(), a, world)
		if err != nil {
			return nil, "", nil, err
		}
		rp, host = worldPackOf(w), libraryHost(w.ID)
	} else {
		_, info, err := s.ownWorldOf(r.Context(), a, template)
		if err != nil {
			return nil, "", nil, err
		}
		rp, host = info.Pack, worldHost(template)
	}
	if rp == nil || rp.Uploaded == nil {
		return nil, "", nil, errBadRequest("That world has no resource pack uploaded to the panel.")
	}
	kept, err := s.keptPack(r.Context(), a.Org.Slug, host)
	if err != nil {
		return nil, "", nil, err
	}
	body, done = s.readSnapshotFile(r.Context(), a.Org.Slug, kept.ID, "/"+packKeptFile)
	return body, rp.Uploaded.Name, done, nil
}

// putWorldPack uploads a resource pack to a world in the library, in place
// of the one it named. Whether players must take it, and what they are
// asked, stay as they were.
func (s *Server) putWorldPack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	world, snap, err := s.libraryWorld(r.Context(), a, r.PathValue("world"))
	if err != nil {
		return err
	}
	release, err := claimWorld(a, world.ID)
	if err != nil {
		return err
	}
	defer release()
	rp, kept, err := s.keepPack(r.Context(), a, libraryHost(world.ID), r.Body, r.URL.Query().Get("name"), u.Username)
	if err != nil {
		return err
	}
	ctx := context.WithoutCancel(r.Context())
	if old := worldPackOf(world); old != nil {
		rp.Enforce, rp.Prompt = old.Enforce, old.Prompt
	}
	world.Pack = packJSON(rp)
	if err := s.Store.SetWorldPack(ctx, a.Org.ID, world.ID, world.Pack); err != nil {
		if derr := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); derr != nil {
			s.Log.Error("could not remove a resource pack kept for nothing", "world", world.ID, "snapshot", kept.ID, "error", derr)
		}
		return err
	}
	s.dropKeptPacks(ctx, a, libraryHost(world.ID), kept.ID)
	s.audit(ctx, u, a.Org, "world.resource-pack", world.Name, rp.SHA1)
	writeJSON(w, http.StatusOK, worldOfSnap(world, snap, false))
	return nil
}

// packChange is a resource pack as a request to change a world's gives it:
// null for none, one from an address, or only whether players must take the
// one it has and what they are asked.
type packChange struct {
	URL     string               `json:"url"`
	SHA1    string               `json:"sha1"`
	Enforce *bool                `json:"enforce"`
	Prompt  *string              `json:"prompt"`
	Project *avsysv1.PackProject `json:"project"`
	Formats *avsysv1.PackFormats `json:"formats"`
}

// changedPack is the pack a world names once raw, a request's resourcePack,
// is applied to the one it named (was).
func changedPack(was *avsysv1.ResourcePackSpec, raw json.RawMessage) (*avsysv1.ResourcePackSpec, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var in packChange
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errBadRequest("A resource pack is null, or an object with a url.")
	}
	var next *avsysv1.ResourcePackSpec
	if in.URL == "" {
		if was == nil {
			return nil, errBadRequest("This world has no resource pack. Give it one with a url, or upload one.")
		}
		next = was.DeepCopy()
	} else {
		if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
			return nil, errBadRequest("The resource pack URL must start with http:// or https://.")
		}
		if in.Project != nil && (in.Project.Source != "modrinth" || in.Project.ID == "") {
			return nil, errBadRequest("A resource pack picked from a project names the project: source modrinth, and the project's id.")
		}
		next = &avsysv1.ResourcePackSpec{URL: strings.TrimSpace(in.URL), SHA1: in.SHA1, Project: in.Project, Formats: in.Formats}
		if err := checkPackSHA1(next, nil); err != nil {
			return nil, err
		}
		if err := checkPackFormats(next, nil); err != nil {
			return nil, err
		}
		if was != nil {
			next.Enforce, next.Prompt = was.Enforce, was.Prompt
		}
	}
	if in.Enforce != nil {
		next.Enforce = *in.Enforce
	}
	if in.Prompt != nil {
		next.Prompt = strings.TrimSpace(*in.Prompt)
	}
	if len([]rune(next.Prompt)) > 500 {
		return nil, errBadRequest("A prompt can be at most 500 characters.")
	}
	return next, nil
}

// giveKeptPack makes rp the server's resource pack, as a world it was given
// names it: an uploaded one read out of where it is kept under host. Whether
// players must take it and what they are asked are rp's.
func (s *Server) giveKeptPack(ctx context.Context, a *access, mc *avsysv1.MinecraftServer, rp *avsysv1.ResourcePackSpec, host string) error {
	if rp == nil {
		return nil
	}
	key := client.ObjectKeyFromObject(mc)
	defer s.lockContent(key)()
	if rp.Uploaded != nil {
		kept, err := s.keptPack(ctx, a.Org.Slug, host)
		if err != nil {
			return err
		}
		body, done := s.readSnapshotFile(ctx, a.Org.Slug, kept.ID, "/"+packKeptFile)
		defer done()
		_, err = s.installPack(ctx, mc, body, rp.Uploaded.Name, false, rp)
		return err
	}
	now, err := s.live(ctx, mc)
	if err != nil {
		return err
	}
	orig := now.DeepCopy()
	next := rp.DeepCopy()
	next.Uploaded, next.Stack = nil, nil
	now.Spec.ResourcePack = next
	if err := s.Client.Patch(ctx, now, client.MergeFrom(orig)); err != nil {
		return err
	}
	// Its uploaded pack before, and those it was joined from, go from its
	// files.
	if keptPacks(orig.Spec.ResourcePack) != nil {
		sc, err := s.workspaces.get(ctx, mc.Namespace, mc.Name)
		s.dropPacks(ctx, sc, err, key, nil, orig.Spec.ResourcePack)
		go s.prunePacksNow(context.WithoutCancel(ctx))
	}
	return nil
}

// carryPack gives a world saved from a server's snapshot (id) the resource
// pack the server had, rp: an uploaded one is read out of the snapshot,
// where the server's files kept it. It says what the world names now.
func (s *Server) carryPack(ctx context.Context, a *access, world, id string, rp *avsysv1.ResourcePackSpec, by string) (string, error) {
	next := &avsysv1.ResourcePackSpec{URL: rp.URL, SHA1: rp.SHA1, Project: rp.Project.DeepCopy(), Formats: rp.Formats.DeepCopy()}
	var kept *backup.Snapshot
	if rp.Uploaded != nil {
		body, done := s.readSnapshotFile(ctx, a.Org.Slug, id, packFile(rp.SHA1))
		up, k, err := s.keepPack(ctx, a, libraryHost(world), body, rp.Uploaded.Name, by)
		done()
		if err != nil {
			return "", err
		}
		next, kept = up, k
	}
	next.Enforce, next.Prompt = rp.Enforce, rp.Prompt
	raw := packJSON(next)
	if err := s.Store.SetWorldPack(ctx, a.Org.ID, world, raw); err != nil {
		if kept != nil {
			if derr := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); derr != nil {
				s.Log.Error("could not remove a resource pack kept for nothing", "world", world, "snapshot", kept.ID, "error", derr)
			}
		}
		return "", err
	}
	return raw, nil
}

// saveName is a world's name as a folder can be called on any computer.
func saveName(name string) string {
	clean := strings.Map(func(c rune) rune {
		if c < 32 || c == 127 || strings.ContainsRune(`<>:"/\|?*`, c) {
			return '_'
		}
		return c
	}, name)
	clean = strings.Trim(clean, " .")
	if clean == "" {
		return "world"
	}
	return clean
}

// zipEntry is where a file of a saved world's archive goes in its zip, in
// the folder name, as a single-player save has it: the world's own files
// at its top, and the Nether and the End, which a server may keep beside
// the world, in it. Anything else in the archive is left out.
func zipEntry(entry, root, name string) (string, bool) {
	entry = strings.TrimSuffix(strings.TrimPrefix(entry, "./"), "/")
	if entry == "" || path.IsAbs(entry) || path.Clean(entry) != entry || strings.HasPrefix(entry, "../") || entry == ".." {
		return "", false
	}
	if entry == root {
		return name, true
	}
	if rest, ok := strings.CutPrefix(entry, root+"/"); ok {
		return name + "/" + rest, true
	}
	for _, d := range []struct{ beside, dim string }{{"_nether", "DIM-1"}, {"_the_end", "DIM1"}} {
		at := root + d.beside + "/" + d.dim
		if entry == at {
			return name + "/" + d.dim, true
		}
		if rest, ok := strings.CutPrefix(entry, at+"/"); ok {
			return name + "/" + d.dim + "/" + rest, true
		}
	}
	return "", false
}

// zipWorld writes the world in a tar, a saved world's archive, into zw, in
// the folder name, and says what it wrote. A file the world has twice, as
// one with its Nether both in it and beside it, is written once.
func zipWorld(zw *zip.Writer, archive io.Reader, root, name string) (map[string]bool, error) {
	written := map[string]bool{}
	tr := tar.NewReader(archive)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return written, nil
		} else if err != nil {
			return written, err
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			continue
		}
		to, ok := zipEntry(h.Name, root, name)
		if !ok {
			continue
		}
		if h.Typeflag == tar.TypeDir {
			to += "/"
		}
		if written[to] {
			continue
		}
		written[to] = true
		fh := &zip.FileHeader{Name: to, Method: zip.Deflate, Modified: h.ModTime}
		if h.Typeflag == tar.TypeDir {
			fh.Method = zip.Store
			fh.SetMode(0o755 | 1<<31)
		} else {
			fh.SetMode(0o644)
		}
		f, err := zw.CreateHeader(fh)
		if err != nil {
			return written, err
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.Copy(f, tr); err != nil {
				return written, err
			}
		}
	}
}

// writeWorldZip writes a world saved from a server or a snapshot, kept in
// snap, to out as a zip of a single-player save, made as it is sent. An
// uploaded pack it names goes in it as the world's own.
func (s *Server) writeWorldZip(ctx context.Context, a *access, world *store.World, snap *backup.Snapshot, out io.Writer) error {
	zw := zip.NewWriter(out)
	// Region files are compressed already: as fast as it goes, not as small.
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) { return flate.NewWriter(w, flate.BestSpeed) })
	name := saveName(world.Name)
	body, done := s.readSnapshotFile(ctx, a.Org.Slug, snap.ID, "/"+ownWorldFile)
	written, err := zipWorld(zw, body, world.Root, name)
	done()
	if err != nil {
		return err
	}
	if len(written) == 0 {
		return errors.New("the world's archive holds no world")
	}
	if rp := worldPackOf(world); rp != nil && rp.Uploaded != nil && !written[name+"/"+packKeptFile] {
		kept, err := s.keptPack(ctx, a.Org.Slug, libraryHost(world.ID))
		if err != nil {
			s.Log.Warn("a world was downloaded without its resource pack", "world", world.ID, "error", err)
		} else {
			f, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/" + packKeptFile, Method: zip.Deflate, Modified: rp.Uploaded.Time.Time})
			if err != nil {
				return err
			}
			body, done := s.readSnapshotFile(ctx, a.Org.Slug, kept.ID, "/"+packKeptFile)
			_, err = io.Copy(f, body)
			done()
			if err != nil {
				return err
			}
		}
	}
	return zw.Close()
}

// packAudit is how a change of a world's pack is noted: what it is now.
func packAudit(rp *avsysv1.ResourcePackSpec) string {
	switch {
	case rp == nil:
		return "removed"
	case rp.SHA1 != "":
		return rp.SHA1
	}
	return rp.URL
}
