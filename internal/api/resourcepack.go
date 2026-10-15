package api

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
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
	"time"

	"github.com/pkg/sftp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// packPath is where an uploaded resource pack is kept in a server's files:
// beside the game's own, so that snapshots and copies take it along.
const packPath = dataRoot + "/.avsys/resource-pack.zip"

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
		if name == "pack.mcmeta" {
			return nil
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
	release, err := s.changing(mc, false)
	if err != nil {
		return err
	}
	defer release()
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

	// Into the server's files first, written beside its place and moved in.
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := writePack(sc, tmp); err != nil {
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
	now := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(r.Context(), client.ObjectKeyFromObject(mc), now); err != nil {
		return err
	}
	orig := now.DeepCopy()
	pack := &avsysv1.ResourcePackSpec{URL: s.packURL(sha), SHA1: sha, Uploaded: &avsysv1.UploadedPack{Name: name, Size: size, Time: metav1.Now()}}
	if old := now.Spec.ResourcePack; old != nil {
		pack.Enforce, pack.Prompt = old.Enforce, old.Prompt
	}
	now.Spec.ResourcePack = pack
	if err := s.Client.Patch(r.Context(), now, client.MergeFrom(orig)); err != nil {
		return err
	}
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.resource-pack", mc.Name, sha)
	writeJSON(w, http.StatusOK, s.view(now))
	return nil
}

// deleteResourcePack takes a server's resource pack away, uploaded or not.
func (s *Server) deleteResourcePack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, false)
	if err != nil {
		return err
	}
	defer release()
	if mc.Spec.ResourcePack != nil && mc.Spec.ResourcePack.Uploaded != nil {
		sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
		if err != nil {
			return err
		}
		if err := sc.Remove(packPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fileError(err)
		}
	}
	now := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(r.Context(), client.ObjectKeyFromObject(mc), now); err != nil {
		return err
	}
	orig := now.DeepCopy()
	now.Spec.ResourcePack = nil
	if err := s.Client.Patch(r.Context(), now, client.MergeFrom(orig)); err != nil {
		return err
	}
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.resource-pack", mc.Name, "removed")
	writeJSON(w, http.StatusOK, s.view(now))
	return nil
}

func writePack(sc *sftp.Client, src io.Reader) error {
	if err := sc.MkdirAll(path.Dir(packPath)); err != nil {
		return fileError(err)
	}
	tmp := packPath + ".avsys-upload"
	f, err := sc.Create(tmp)
	if err != nil {
		return fileError(err)
	}
	_, err = f.ReadFrom(src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sc.PosixRename(tmp, packPath)
	}
	if err != nil {
		_ = sc.Remove(tmp)
		return fileError(err)
	}
	return nil
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
			if err = s.fetchPack(r.Context(), sha); err == nil {
				f, err = os.Open(s.cachedPack(sha))
			}
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
	src, err := sc.Open(packPath)
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
