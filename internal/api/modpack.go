package api

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A modpack can be a Modrinth pack file, a .mrpack, uploaded through the
// panel. It is kept on the server's own volume, in its content folder, by
// its SHA-1, so that it goes wherever the server's files go: snapshots,
// copies, templates, restores. The image installs the pack from there.

// mrpackIndex is what the panel reads of a pack's modrinth.index.json.
type mrpackIndex struct {
	Game         string            `json:"game"`
	VersionID    string            `json:"versionId"`
	Name         string            `json:"name"`
	Dependencies map[string]string `json:"dependencies"`
	Files        []struct {
		Path      string   `json:"path"`
		Downloads []string `json:"downloads"`
	} `json:"files"`
}

// mrpackLoaders are the loaders a pack can say it needs, by the name the
// pack gives each, as the panel names them.
var mrpackLoaders = map[string]string{
	"fabric-loader": "fabric",
	"quilt-loader":  "quilt",
	"forge":         "forge",
	"neoforge":      "neoforge",
}

// maxIndex bounds how much of a pack's index is read.
const maxIndex = 32 << 20

var errNotMrpack = errBadRequest("This is not a Modrinth modpack (.mrpack).")

// readMrpack reads what a pack is from its index: its name, version, and the
// game version and loader it is built for. It says plainly what is wrong
// with one the image could not install.
func readMrpack(r io.ReaderAt, size int64) (*avsysv1.ModpackSpec, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, errNotMrpack
	}
	var index *zip.File
	for _, f := range zr.File {
		if f.Name == "modrinth.index.json" {
			index = f
			break
		}
	}
	if index == nil {
		return nil, errNotMrpack
	}
	rc, err := index.Open()
	if err != nil {
		return nil, errNotMrpack
	}
	defer rc.Close()
	var idx mrpackIndex
	if err := json.NewDecoder(io.LimitReader(rc, maxIndex)).Decode(&idx); err != nil {
		return nil, errBadRequest("The modpack's modrinth.index.json cannot be read.")
	}
	if idx.Game != "minecraft" {
		return nil, errBadRequest("This modpack is not for Minecraft.")
	}
	game := strings.TrimSpace(idx.Dependencies["minecraft"])
	if game == "" {
		return nil, errBadRequest("The modpack does not say which Minecraft version it is for.")
	}
	loader := ""
	for dep, name := range mrpackLoaders {
		if idx.Dependencies[dep] != "" {
			loader = name
			break
		}
	}
	if loader == "" {
		return nil, errBadRequest("The modpack needs Fabric, Quilt, Forge or NeoForge.")
	}
	for _, f := range idx.Files {
		p := strings.ReplaceAll(f.Path, "\\", "/")
		if p == "" || path.IsAbs(p) || p != path.Clean(p) || p == ".." || strings.HasPrefix(p, "../") {
			return nil, errBadRequest("The modpack lists a file outside the server's folder: %q.", f.Path)
		}
		if len(f.Downloads) == 0 {
			return nil, errBadRequest("The modpack lists %s without a download.", path.Base(p))
		}
	}
	return &avsysv1.ModpackSpec{
		Source:      avsysv1.SourceUpload,
		Title:       strings.TrimSpace(idx.Name),
		VersionName: strings.TrimSpace(idx.VersionID),
		GameVersion: game,
		Loader:      loader,
	}, nil
}

// modpackFilename makes an uploaded pack's name safe to keep and show.
func modpackFilename(name string) (string, error) {
	name = strings.TrimSpace(unsafeFilenameChars.ReplaceAllString(path.Base(strings.ReplaceAll(name, "\\", "/")), "_"))
	const want = ".mrpack"
	if !strings.HasSuffix(strings.ToLower(name), want) || len(name) <= len(want) || len(name) > 200 || strings.HasPrefix(name, ".") {
		return "", errBadRequest("Upload a %s file.", want)
	}
	return name, nil
}

// checkUploadedModpack checks an uploaded modpack in settings: its name is one
// the panel gives, and its version, where there is one, is a SHA-1, which
// names its file.
func checkUploadedModpack(p *avsysv1.ModpackSpec) error {
	if p == nil || p.Source != avsysv1.SourceUpload {
		return nil
	}
	if name, err := modpackFilename(p.Project); err != nil || name != p.Project {
		return errBadRequest("%q is not an uploaded modpack.", p.Project)
	}
	if p.Version != "" && p.UploadedFile() == "" {
		return errBadRequest("An uploaded modpack's version is the SHA-1 of its file.")
	}
	return nil
}

// keepUploadedModpack holds a change of settings to an uploaded modpack: it
// is kept as it is, and another file is only had by uploading it.
func keepUploadedModpack(spec, previous *avsysv1.MinecraftServerSpec) error {
	p, was := spec.Modpack, previous.Modpack
	if p == nil || p.Source != avsysv1.SourceUpload {
		return nil
	}
	if was == nil || was.Source != avsysv1.SourceUpload || was.Version != p.Version || was.Project != p.Project {
		return errBadRequest("A modpack file is uploaded with PUT /orgs/{org}/servers/{server}/modpack.")
	}
	// What is known of it is what its file said.
	spec.Modpack = was.DeepCopy()
	return nil
}

// modpackMissing says why a server cannot start: its modpack is one to
// upload that has not been.
func modpackMissing(spec *avsysv1.MinecraftServerSpec) error {
	if p := spec.Modpack; p != nil && p.Source == avsysv1.SourceUpload && p.UploadedFile() == "" {
		return &Error{Status: http.StatusConflict, Code: "no_modpack", Message: "Upload the modpack's .mrpack file on the Content page first."}
	}
	return nil
}

// finalState is the state a server made from a snapshot or another server
// is left in once its files are there: the one asked for, except that one
// whose modpack file was never uploaded, as its original's was not, stays
// stopped until it is. It has nothing to start with.
func finalState(spec *avsysv1.MinecraftServerSpec, asked avsysv1.DesiredState) avsysv1.DesiredState {
	if modpackMissing(spec) != nil {
		return avsysv1.StateStopped
	}
	return asked
}

// putModpack takes an uploaded .mrpack as the server's modpack: it is read,
// kept on the server's volume by its SHA-1, and the server's settings say
// it, with what it is built for. The one before is removed. The body is the
// file, or ?upload= names one sent in parts to the organization, of ?size=.
//
// With ?wait=true it is for a server made a moment ago, made stopped to be
// given it first: its volume is waited for.
func (s *Server) putModpack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	q := r.URL.Query()
	wait := q.Get("wait") == "true"
	a, mc, err := s.serverMaybeNew(r, u, wait)
	if err != nil {
		return err
	}
	if q.Get("upload") != "" {
		defer s.dropStaged(a, q.Get("upload"))
	}
	if mc.Spec.Modpack == nil {
		return errBadRequest("This server does not run a modpack.")
	}
	// Not while it is snapshotted: the snapshot's record of its modpack and
	// the file in it would not be of the same pack.
	release, err := s.changing(mc, true)
	if err != nil {
		return err
	}
	defer release()
	defer s.lockContent(client.ObjectKeyFromObject(mc))()
	name, err := modpackFilename(q.Get("name"))
	if err != nil {
		return err
	}
	body, done, err := s.uploadBody(w, r, a)
	if err != nil {
		return err
	}
	defer done()

	sc, closeFiles, err := s.filesOf(r.Context(), mc, wait)
	if err != nil {
		s.Log.Warn("workspace unreachable", "server", mc.Name, "error", err)
		return errWorkspace
	}
	defer closeFiles()
	if err := sc.MkdirAll(controller.ContentDir); err != nil {
		return fileError(err)
	}
	// There must be room for it on the server's volume.
	size := r.ContentLength
	if q.Get("upload") != "" {
		size, _ = strconv.ParseInt(q.Get("size"), 10, 64)
	}
	if vfs, err := sc.StatVFS(controller.ContentDir); err == nil && size > 0 {
		if free := int64(vfs.Bavail) * int64(vfs.Frsize); size > free {
			return errNoRoom(size, free, "the server")
		}
	}
	// Written beside its place, read, and moved in.
	tmp := fmt.Sprintf("%s/.avsys-upload-modpack-%d", controller.ContentDir, time.Now().UnixNano())
	f, err := sc.Create(tmp)
	if err != nil {
		return fileError(err)
	}
	placed := false
	defer func() {
		if !placed {
			_ = sc.Remove(tmp)
		}
	}()
	h := sha1.New()
	n, err := f.ReadFromWithConcurrency(io.TeeReader(body, h), 0)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errBadRequest("Over 2 GB in one request: send it in parts.")
		}
		return errBadRequest("The upload did not complete: %v.", err)
	}
	if n == 0 {
		return errBadRequest("The upload was empty.")
	}
	rf, err := sc.Open(tmp)
	if err != nil {
		return fileError(err)
	}
	pack, err := readMrpack(rf, n)
	rf.Close()
	if err != nil {
		return err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	pack.Project, pack.Version = name, sha
	if pack.Title == "" {
		pack.Title = strings.TrimSuffix(name, path.Ext(name))
	}
	file := path.Join(controller.ContentDir, pack.UploadedFile())
	// One there already, of the same pack uploaded before, may be what the
	// settings say: it is never removed for a failure here.
	_, statErr := sc.Stat(file)
	had := statErr == nil
	if err := sc.PosixRename(tmp, file); err != nil {
		return fileError(err)
	}
	placed = true

	// The file is in place; now the settings say it. Someone may be changing
	// the server at the same moment, so a conflicting write is tried again
	// on what is there then.
	var previous *avsysv1.MinecraftServerSpec
	for attempt := 0; ; attempt++ {
		if mc, err = s.live(r.Context(), mc); err == nil && mc.Spec.Modpack == nil {
			err = errBadRequest("This server does not run a modpack.")
		}
		if err == nil {
			previous = mc.Spec.DeepCopy()
			mc.Spec.Modpack = pack.DeepCopy()
			// Built for an older game than its world, which it then cannot
			// open: only when the request says that is meant, as for another
			// version of a pack.
			if controller.GameVersionOf(&mc.Spec) != controller.GameVersionOf(previous) {
				err = s.checkWorldVersion(r, s.ranOn(r.Context(), mc, previous), &mc.Spec)
			}
		}
		if err == nil {
			// Content on top of it fits what it is built for.
			err = s.resolveContent(r.Context(), &mc.Spec, previous)
		}
		if err == nil {
			err = s.Client.Update(r.Context(), mc)
		}
		if err == nil {
			break
		}
		if !apierrors.IsConflict(err) || attempt == 3 {
			// The settings still say the pack before: the new file goes,
			// unless it was there already.
			if !had {
				_ = sc.Remove(file)
			}
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.removeDroppedUploadsWith(r.Context(), sc, a.Namespace, mc.Name, &mc.Spec, previous)
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.modpack", mc.Name, name)
	writeJSON(w, http.StatusOK, s.view(mc))
	return nil
}
