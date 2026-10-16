package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// packDir is where an uploaded resource pack is kept in a server's files:
// beside the game's own, so that snapshots and copies take it along. Each is
// named by its hash, so that the file and the server's settings never
// speak of different packs: a new one is written beside the old, and the
// old is removed only once the settings say the new one.
const packDir = dataRoot + "/.avsys"

func packFile(sha string) string { return packDir + "/resource-pack-" + sha + ".zip" }

// packLimit is the most the game takes of a server resource pack, by the
// game version it runs: 250 MB from 1.18, 100 MB from 1.15, and 50 MB
// before. A version not given as 1.x, a snapshot or "latest", is taken as
// new.
func packLimit(version string) int64 {
	const mb = 1 << 20
	m := regexp.MustCompile(`^1\.(\d+)`).FindStringSubmatch(version)
	if m == nil {
		return 250 * mb
	}
	minor, _ := strconv.Atoi(m[1])
	switch {
	case minor >= 18:
		return 250 * mb
	case minor >= 15:
		return 100 * mb
	default:
		return 50 * mb
	}
}

// checkPack says whether a file is a resource pack the game can use: a zip
// with pack.mcmeta at its top. One whose pack is a folder down is the usual
// mistake, and is told so.
func checkPack(f *os.File, size int64) error {
	z, err := zip.NewReader(f, size)
	if err != nil {
		return errBadRequest("That is not a zip file. A resource pack is a .zip with pack.mcmeta at its top.")
	}
	folders := map[string]bool{}
	var nested string
	for _, e := range z.File {
		name := strings.TrimPrefix(e.Name, "./")
		if strings.TrimSuffix(name, "/") == "pack.mcmeta" {
			return checkMeta(e)
		}
		if i := strings.Index(name, "/"); i > 0 {
			folders[name[:i]] = true
			if name[i+1:] == "pack.mcmeta" {
				nested = name[:i]
			}
		}
	}
	if nested != "" && len(folders) == 1 {
		return errBadRequest("The pack is in a folder, %s, inside the zip. Zip what is inside that folder instead, so that pack.mcmeta is at the top.", nested)
	}
	return errBadRequest("There is no pack.mcmeta at the top of that zip, so it is not a resource pack.")
}

// packFits says whether a server's uploaded pack is one the game of its
// version takes, as after the version is changed.
func packFits(spec *avsysv1.MinecraftServerSpec) error {
	if rp := spec.ResourcePack; rp != nil && rp.Uploaded != nil && rp.Uploaded.Size > packLimit(spec.Version) {
		return errBadRequest("Its resource pack is over %d MB, the most the game takes for %s. Upload a smaller one, or remove it first.", packLimit(spec.Version)>>20, spec.Version)
	}
	return nil
}

// checkMeta says whether pack.mcmeta is what the game reads it as: a file of
// JSON with a pack in it.
func checkMeta(e *zip.File) error {
	bad := errBadRequest("pack.mcmeta in that zip is not a pack's metadata: the game could not read it.")
	if e.FileInfo().IsDir() {
		return bad
	}
	f, err := e.Open()
	if err != nil {
		return bad
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return bad
	}
	// A pack has a description, and the formats it is for: pack_format,
	// or min_format from the game's 1.21.9 on.
	var meta struct {
		Pack *struct {
			Description json.RawMessage `json:"description"`
			Format      json.Number     `json:"pack_format"`
			MinFormat   json.RawMessage `json:"min_format"`
		} `json:"pack"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if d.Decode(&meta) != nil || meta.Pack == nil || len(meta.Pack.Description) == 0 || (meta.Pack.Format == "" && len(meta.Pack.MinFormat) == 0) {
		return bad
	}
	return nil
}

// packURL is where the panel serves a pack of this SHA-1.
func (s *Server) packURL(sha string) string {
	base := s.Config.PackURL
	if base == "" {
		base = s.Config.PublicURL
	}
	return strings.TrimRight(base, "/") + "/packs/" + sha + ".zip"
}

// putResourcePack takes an uploaded pack as the server's resource pack: it
// is checked, kept in the server's files and in the panel's own, and served.
// The server's pack settings, required or not and the prompt, are kept.
func (s *Server) putResourcePack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	// Not while it is snapshotted either: the snapshot's record of its pack
	// and the file in it would not be of the same pack.
	release, err := s.changing(mc, true)
	if err != nil {
		return err
	}
	defer release()
	// One change to its files at a time: content, and its pack.
	defer s.lockContent(client.ObjectKeyFromObject(mc))()
	if s.Config.PackDir == "" {
		return &Error{Status: http.StatusServiceUnavailable, Code: "packs_unavailable", Message: "This panel has nowhere to keep resource packs."}
	}
	limit := packLimit(mc.Spec.Version)
	if err := os.MkdirAll(s.Config.PackDir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Config.PackDir, "upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha1.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r.Body, limit+1))
	if err != nil {
		return errBadRequest("The upload did not arrive whole.")
	}
	if size > limit {
		return errBadRequest("That pack is over %d MB, the most the game takes for %s.", limit>>20, mc.Spec.Version)
	}
	if size == 0 {
		return errBadRequest("The upload was empty.")
	}
	if err := checkPack(tmp, size); err != nil {
		return err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	// Its version as it is now, which a change a moment ago may have
	// lowered, before anything is written.
	now, err := s.live(r.Context(), mc)
	if err != nil {
		return err
	}
	if limit := packLimit(now.Spec.Version); size > limit {
		return errBadRequest("That pack is over %d MB, the most the game takes for %s.", limit>>20, now.Spec.Version)
	}

	// Into the server's files first, written beside its place and moved in.
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := writePack(sc, tmp, sha); err != nil {
		return err
	}
	// And kept by the panel, under its hash, to serve.
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.cachedPack(sha)); err != nil {
		return err
	}

	name := path.Base(strings.ReplaceAll(r.URL.Query().Get("name"), "\\", "/"))
	if name == "." || name == "/" {
		name = ""
	}
	orig := now.DeepCopy()
	pack := &avsysv1.ResourcePackSpec{URL: s.packURL(sha), SHA1: sha, Uploaded: &avsysv1.UploadedPack{Name: name, Size: size, Time: metav1.Now()}}
	if old := now.Spec.ResourcePack; old != nil {
		pack.Enforce, pack.Prompt = old.Enforce, old.Prompt
	}
	was := orig.Spec.ResourcePack
	now.Spec.ResourcePack = pack
	if err := s.Client.Patch(r.Context(), now, client.MergeFrom(orig)); err != nil {
		// The settings still say the pack before: the new file goes.
		if was == nil || was.SHA1 != sha {
			_ = sc.Remove(packFile(sha))
		}
		return err
	}
	// The settings say the new pack: any other is gone from them.
	removeOtherPacks(sc, sha)
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.resource-pack", mc.Name, sha)
	go s.prunePacksNow(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusOK, s.view(now))
	return nil
}

// deleteResourcePack takes a server's resource pack away, uploaded or not.
func (s *Server) deleteResourcePack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, true)
	if err != nil {
		return err
	}
	defer release()
	defer s.lockContent(client.ObjectKeyFromObject(mc))()
	// As it is now: an upload a moment ago may not be in the cache yet.
	if mc, err = s.live(r.Context(), mc); err != nil {
		return err
	}
	uploaded := mc.Spec.ResourcePack != nil && mc.Spec.ResourcePack.Uploaded != nil
	now := mc.DeepCopy()
	orig := now.DeepCopy()
	now.Spec.ResourcePack = nil
	// The settings first: a file left over when they no longer say it is
	// only a file, removed by the next upload if not now.
	if err := s.Client.Patch(r.Context(), now, client.MergeFrom(orig)); err != nil {
		return err
	}
	if uploaded {
		if sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name); err == nil {
			removeOtherPacks(sc, "")
		}
	}
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.resource-pack", mc.Name, "removed")
	go s.prunePacksNow(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusOK, s.view(now))
	return nil
}

func writePack(sc *sftp.Client, src io.Reader, sha string) error {
	if err := sc.MkdirAll(packDir); err != nil {
		return fileError(err)
	}
	tmp := fmt.Sprintf("%s.avsys-upload-%d", packFile(sha), time.Now().UnixNano())
	f, err := sc.Create(tmp)
	if err != nil {
		return fileError(err)
	}
	// Many writes in flight at once, not one round trip at a time: a hole
	// that a failure leaves is only ever in a file that is then removed.
	_, err = f.ReadFromWithConcurrency(src, 0)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sc.PosixRename(tmp, packFile(sha))
	}
	if err != nil {
		_ = sc.Remove(tmp)
		return fileError(err)
	}
	return nil
}

// packGrace is how long a pack the panel keeps is left alone however it
// looks: one just uploaded is kept before the server's settings say so.
const packGrace = 10 * time.Minute

// prunePacks deletes the packs the panel keeps that none of these servers
// has, and no game of theirs still offers: one running from before its pack
// was replaced offers the old one until it is restarted. One brought back
// on a server later, from a snapshot, is read out of its files again when
// it is first asked for.
func (s *Server) prunePacks(ctx context.Context, servers []avsysv1.MinecraftServer) {
	if s.Config.PackDir == "" {
		return
	}
	used := map[string]bool{}
	for i := range servers {
		if rp := servers[i].Spec.ResourcePack; rp != nil && rp.Uploaded != nil {
			used[rp.SHA1] = true
		}
	}
	var pods corev1.PodList
	if err := s.nodeReader().List(ctx, &pods, client.HasLabels{avsysv1.LabelServer}); err != nil {
		// Not knowing what games offer, nothing is pruned.
		return
	}
	for _, pod := range pods.Items {
		for _, c := range pod.Spec.Containers {
			for _, e := range c.Env {
				if e.Name == "RESOURCE_PACK_SHA1" && e.Value != "" {
					used[e.Value] = true
				}
			}
		}
	}
	entries, err := os.ReadDir(s.Config.PackDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		m := packName.FindStringSubmatch(e.Name())
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < packGrace {
			continue
		}
		// Kept, or what an upload or fetch left behind when it failed.
		if (m != nil && used[m[1]]) || (m == nil && !strings.HasPrefix(e.Name(), "upload-") && !strings.HasPrefix(e.Name(), "fetch-")) {
			continue
		}
		if err := os.Remove(filepath.Join(s.Config.PackDir, e.Name())); err == nil {
			s.Log.Info("removed a resource pack no server has", "file", e.Name())
		}
	}
}

// prunePacksNow is prunePacks over every server, for after a pack changed.
func (s *Server) prunePacksNow(ctx context.Context) {
	var all avsysv1.MinecraftServerList
	if err := s.nodeReader().List(ctx, &all); err == nil {
		s.prunePacks(ctx, all.Items)
	}
}

// removeOtherPacks removes the uploaded packs in a server's files but the one
// of this hash, as with "" all of them.
func removeOtherPacks(sc *sftp.Client, keep string) {
	entries, err := sc.ReadDir(packDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "resource-pack-") && name != path.Base(packFile(keep)) {
			_ = sc.Remove(packDir + "/" + name)
		}
	}
}

// removeDroppedPack removes the file of an uploaded pack a server's settings
// no longer have, after they were changed to one hosted elsewhere.
func (s *Server) removeDroppedPack(ctx context.Context, namespace, name string, spec, previous *avsysv1.MinecraftServerSpec) {
	was, now := previous.ResourcePack, spec.ResourcePack
	if was == nil || was.Uploaded == nil || (now != nil && now.Uploaded != nil && now.SHA1 == was.SHA1) {
		return
	}
	if sc, err := s.workspaces.get(ctx, namespace, name); err == nil {
		removeOtherPacks(sc, "")
	}
}

func (s *Server) cachedPack(sha string) string {
	return filepath.Join(s.Config.PackDir, sha+".zip")
}

var packName = regexp.MustCompile(`^([0-9a-f]{40})\.zip$`)

// PackHandler serves uploaded resource packs to players' games, which fetch
// them with no session: /packs/<sha1>.zip and nothing else. A pack's address
// is its hash, so the file there never changes. One the panel has not kept,
// as after it was brought back from a snapshot, is read out of the files of
// a server that has it.
func (s *Server) PackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m := packName.FindStringSubmatch(strings.TrimPrefix(r.URL.Path, "/packs/"))
		if m == nil || !strings.HasPrefix(r.URL.Path, "/packs/") || s.Config.PackDir == "" {
			http.NotFound(w, r)
			return
		}
		sha := m[1]
		f, err := os.Open(s.cachedPack(sha))
		if errors.Is(err, fs.ErrNotExist) {
			// One copy, which everyone asking meanwhile waits for.
			mu, _ := s.packFetches.LoadOrStore(sha, &sync.Mutex{})
			mu.(*sync.Mutex).Lock()
			if f, err = os.Open(s.cachedPack(sha)); errors.Is(err, fs.ErrNotExist) {
				if err = s.fetchPack(r.Context(), sha); err == nil {
					f, err = os.Open(s.cachedPack(sha))
				}
			}
			mu.(*sync.Mutex).Unlock()
			// Not kept: any hash at all may be asked for here.
			s.packFetches.Delete(sha)
		}
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errNoSuchPack) {
				s.Log.Warn("serving a resource pack", "sha1", sha, "error", err)
			}
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, sha+".zip", info.ModTime(), f)
	})
}

var errNoSuchPack = errors.New("no server has that resource pack")

// fetchPack reads a pack the panel has not kept out of the files of a server
// whose settings say it has it, and keeps it, if what is there has that hash.
func (s *Server) fetchPack(ctx context.Context, sha string) error {
	var list avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &list); err != nil {
		return err
	}
	for i := range list.Items {
		mc := &list.Items[i]
		rp := mc.Spec.ResourcePack
		if rp == nil || rp.Uploaded == nil || rp.SHA1 != sha {
			continue
		}
		if err := s.copyPackFrom(ctx, mc, sha); err == nil {
			return nil
		} else {
			s.Log.Info("could not read a resource pack from a server's files", "server", mc.Namespace+"/"+mc.Name, "error", err)
		}
	}
	return errNoSuchPack
}

func (s *Server) copyPackFrom(ctx context.Context, mc *avsysv1.MinecraftServer, sha string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	sc, err := s.workspaces.get(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return err
	}
	src, err := sc.Open(packFile(sha))
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(s.Config.PackDir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Config.PackDir, "fetch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha1.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(src, packLimit("")+1)); err != nil {
		return err
	}
	// The server's files are anyone's with a shell there: what is served at
	// a hash is only ever what has it.
	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return fmt.Errorf("its pack has changed since it was uploaded (now %s)", got[:8])
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.cachedPack(sha))
}
