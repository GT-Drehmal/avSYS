package api

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/packmerge"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A server can offer players only one resource pack, so several are joined
// into one: the stack. Each pack in it is kept in the server's files as an
// uploaded pack is, by its hash, and so is the pack joined from them, which
// the panel serves. Snapshots and copies take them all along, and the pack
// can be joined again wherever they are.

// maxStack is the most packs a stack has.
const maxStack = 32

// addToStack puts an uploaded pack on top of the server's stack. A server
// with one pack, not stacked yet, has it as the one under.
func (s *Server) addToStack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	return s.addPackTo(w, r, u, func(to *os.File, limit int64) (stacked avsysv1.StackedPack, err error) {
		size, sha, err := copyHashed(to, r.Body, limit)
		if err != nil {
			return stacked, errBadRequest("The upload was incomplete.")
		}
		name := path.Base(strings.ReplaceAll(r.URL.Query().Get("name"), "\\", "/"))
		if name == "." || name == "/" {
			name = ""
		}
		return avsysv1.StackedPack{SHA1: sha, Size: size, Name: name}, nil
	})
}

// addURLToStack puts a pack from an address on top of the server's stack:
// one picked from Modrinth, with its project, or any other.
func (s *Server) addURLToStack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct {
		URL     string               `json:"url"`
		SHA1    string               `json:"sha1"`
		Project *avsysv1.PackProject `json:"project"`
		Formats *avsysv1.PackFormats `json:"formats"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Project != nil && (in.Project.Source != "modrinth" || in.Project.ID == "") {
		return errBadRequest("A resource pack picked from a project names the project: source modrinth, and the project's id.")
	}
	return s.addPackTo(w, r, u, func(to *os.File, limit int64) (avsysv1.StackedPack, error) {
		stacked, err := downloadPack(r.Context(), to, in.URL, in.SHA1, limit)
		stacked.Project = in.Project
		return stacked, err
	})
}

// addPackTo puts the pack that get writes to a file on top of the server's
// stack. get is given how large it may be. With ?wait=true it is for a
// server made a moment ago, given its packs before it first starts: its
// volume is waited for.
func (s *Server) addPackTo(w http.ResponseWriter, r *http.Request, u *store.User, get func(to *os.File, limit int64) (avsysv1.StackedPack, error)) error {
	wait := r.URL.Query().Get("wait") == "true"
	a, mc, err := s.serverMaybeNew(r, u, wait)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, true)
	if err != nil {
		return err
	}
	defer release()
	defer s.lockContent(client.ObjectKeyFromObject(mc))()
	if s.Config.PackDir == "" {
		return &Error{Status: http.StatusServiceUnavailable, Code: "packs_unavailable", Message: "Resource pack uploads are not set up on this panel."}
	}
	tmp, err := s.packTemp("upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	game := controller.GameVersionOf(&mc.Spec)
	limit := packLimit(game)
	top, err := get(tmp, limit)
	if err != nil {
		return err
	}
	if top.Size > limit {
		return errBadRequest("That pack is over %d MB, the most the game takes for %s.", limit>>20, game)
	}
	if top.Size == 0 {
		return errBadRequest("That pack is empty.")
	}
	if top.Formats, err = checkPack(tmp, top.Size); err != nil {
		return err
	}
	now, err := s.live(r.Context(), mc)
	if err != nil {
		return err
	}
	sc, done, err := s.filesOf(r.Context(), now, wait)
	if err != nil {
		return err
	}
	defer done()
	stack, err := s.stackOf(r.Context(), sc, now.Spec.ResourcePack, limit)
	if err != nil {
		return err
	}
	for _, p := range stack {
		if p.SHA1 == top.SHA1 {
			return errBadRequest("That pack is in the list already.")
		}
	}
	if len(stack) >= maxStack {
		return errBadRequest("A server can have at most %d resource packs.", maxStack)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := writePack(sc, tmp, top.SHA1); err != nil {
		return err
	}
	if now, err = s.restack(r.Context(), now, sc, append([]avsysv1.StackedPack{top}, stack...)); err != nil {
		// Its file is kept only while a stack has it.
		if !inStack(now.Spec.ResourcePack, top.SHA1) {
			_ = sc.Remove(packFile(top.SHA1))
		}
		return err
	}
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.resource-pack", mc.Name, "added "+top.SHA1)
	go s.prunePacksNow(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusOK, s.view(now))
	return nil
}

// orderStack puts the server's stack in the order given, by the packs'
// SHA-1, the top one first; those left out are taken off it.
func (s *Server) orderStack(w http.ResponseWriter, r *http.Request, u *store.User) error {
	var in struct {
		Order []string `json:"order"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	// As for adding one, with ?wait=true.
	wait := r.URL.Query().Get("wait") == "true"
	a, mc, err := s.serverMaybeNew(r, u, wait)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, true)
	if err != nil {
		return err
	}
	defer release()
	defer s.lockContent(client.ObjectKeyFromObject(mc))()
	now, err := s.live(r.Context(), mc)
	if err != nil {
		return err
	}
	rp := now.Spec.ResourcePack
	if rp == nil || len(rp.Stack) == 0 {
		return errBadRequest("This server's resource pack is not joined from several.")
	}
	by := map[string]avsysv1.StackedPack{}
	for _, p := range rp.Stack {
		by[p.SHA1] = p
	}
	order := make([]avsysv1.StackedPack, 0, len(in.Order))
	seen := map[string]bool{}
	for _, sha := range in.Order {
		p, ok := by[sha]
		if !ok {
			return errBadRequest("%s is not one of this server's resource packs.", sha)
		}
		if seen[sha] {
			return errBadRequest("%s is listed twice.", sha)
		}
		seen[sha] = true
		order = append(order, p)
	}
	same := len(order) == len(rp.Stack)
	for i := range order {
		same = same && order[i].SHA1 == rp.Stack[i].SHA1
	}
	if same {
		writeJSON(w, http.StatusOK, s.view(now))
		return nil
	}
	sc, done, err := s.filesOf(r.Context(), now, wait)
	if err != nil {
		return err
	}
	defer done()
	if now, err = s.restack(r.Context(), now, sc, order); err != nil {
		return err
	}
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.resource-pack", mc.Name, "ordered "+strings.Join(in.Order, ","))
	go s.prunePacksNow(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusOK, s.view(now))
	return nil
}

// stackOf is the packs a server's resource pack is joined from, the top one
// first. One not joined is the only one: an uploaded one's file is there
// already, and one from an address is fetched into its files.
func (s *Server) stackOf(ctx context.Context, sc *sftp.Client, rp *avsysv1.ResourcePackSpec, limit int64) ([]avsysv1.StackedPack, error) {
	switch {
	case rp == nil || rp.URL == "":
		return nil, nil
	case len(rp.Stack) > 0:
		return append([]avsysv1.StackedPack(nil), rp.Stack...), nil
	case rp.Uploaded != nil:
		return []avsysv1.StackedPack{{SHA1: rp.SHA1, Size: rp.Uploaded.Size, Name: rp.Uploaded.Name, Formats: rp.Formats}}, nil
	}
	tmp, err := s.packTemp("fetch-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	p, err := downloadPack(ctx, tmp, rp.URL, rp.SHA1, limit)
	if err != nil {
		return nil, fmt.Errorf("the resource pack it has now could not be fetched to join it with: %w", err)
	}
	if _, err := checkPack(tmp, p.Size); err != nil {
		return nil, fmt.Errorf("the resource pack it has now is not one that can be joined: %w", err)
	}
	p.Project, p.Formats = rp.Project, rp.Formats
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := writePack(sc, tmp, p.SHA1); err != nil {
		return nil, err
	}
	return []avsysv1.StackedPack{p}, nil
}

// restack joins these packs, whose files are in the server's files, the top
// one first, and makes the pack joined the server's: served by the panel,
// and kept in its files beside them. Its prompt, and whether it is
// required, are kept. With none, it has none.
func (s *Server) restack(ctx context.Context, now *avsysv1.MinecraftServer, sc *sftp.Client, stack []avsysv1.StackedPack) (*avsysv1.MinecraftServer, error) {
	orig := now.DeepCopy()
	was := orig.Spec.ResourcePack
	next := now.DeepCopy()
	if len(stack) == 0 {
		next.Spec.ResourcePack = nil
	} else {
		// For the game it runs, which a modpack's says.
		rp, err := s.joinStack(ctx, sc, stack, controller.GameVersionOf(&now.Spec))
		if err != nil {
			return now, err
		}
		if was != nil {
			rp.Enforce, rp.Prompt = was.Enforce, was.Prompt
		}
		next.Spec.ResourcePack = rp
	}
	if err := s.Client.Patch(ctx, next, client.MergeFrom(orig)); err != nil {
		if rp := next.Spec.ResourcePack; rp != nil && (was == nil || was.SHA1 != rp.SHA1) {
			_ = sc.Remove(packFile(rp.SHA1))
		}
		return now, err
	}
	s.dropPacks(ctx, sc, nil, client.ObjectKeyFromObject(now), keptPacks(next.Spec.ResourcePack), was)
	return next, nil
}

// joinStack joins the packs, from the server's files, into one for a server
// of this game version: kept in its files and by the panel.
func (s *Server) joinStack(ctx context.Context, sc *sftp.Client, stack []avsysv1.StackedPack, version string) (*avsysv1.ResourcePackSpec, error) {
	packs := make([]packmerge.Pack, len(stack))
	for i, p := range stack {
		local, err := s.packTemp("stack-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(local.Name())
		defer local.Close()
		src, err := sc.Open(packFile(p.SHA1))
		if err != nil {
			return nil, fmt.Errorf("the file of %s is not in the server's files: %w", stackedName(p), fileError(err))
		}
		size, sha, err := readPackFile(local, src, packLimit(""))
		src.Close()
		if err != nil {
			return nil, fileError(err)
		}
		// The server's files are anyone's with a shell there: a pack is
		// joined only as it was when it was added.
		if sha != p.SHA1 {
			return nil, errBadRequest("The file of %s has changed since it was added. Take the pack off and add it again.", stackedName(p))
		}
		z, err := zip.NewReader(local, size)
		if err != nil {
			return nil, errBadRequest("The file of %s is no longer a zip.", stackedName(p))
		}
		packs[i] = packmerge.Pack{Name: stackedName(p), Zip: z}
	}
	// Made for the game it is offered by, where its format is known.
	format, err := s.packFormatOf(ctx, version)
	if err != nil {
		format = 0
	}
	joined, err := s.packTemp("upload-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(joined.Name())
	defer joined.Close()
	h := sha1.New()
	counted := &countWriter{}
	res, err := packmerge.Merge(io.MultiWriter(joined, h, counted), packs, int(format))
	if err != nil {
		return nil, errBadRequest("The packs could not be joined: %v", err)
	}
	if limit := packLimit(version); counted.n > limit {
		return nil, errBadRequest("Joined, the packs are over %d MB, the most the game takes for %s.", limit>>20, version)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	if _, err := joined.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := writePack(sc, joined, sha); err != nil {
		return nil, err
	}
	if err := joined.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(joined.Name(), s.cachedPack(sha)); err != nil {
		return nil, err
	}
	stack = append([]avsysv1.StackedPack(nil), stack...)
	most := map[int]int{}
	for i := range stack {
		stack[i].Hidden, stack[i].HiddenBy = 0, ""
	}
	for _, h := range res.Hidden {
		stack[h.Under].Hidden += int32(len(h.Files))
		if len(h.Files) > most[h.Under] {
			most[h.Under] = len(h.Files)
			stack[h.Under].HiddenBy = stack[h.Over].SHA1
		}
	}
	rp := &avsysv1.ResourcePackSpec{
		URL:      s.packURL(sha),
		SHA1:     sha,
		Uploaded: &avsysv1.UploadedPack{Size: counted.n, Time: metav1.Now()},
		Stack:    stack,
	}
	if format > 0 {
		rp.Formats = &avsysv1.PackFormats{Min: format, Max: format}
	}
	return rp, nil
}

// stackOutdated says whether a server's pack joined from several is made
// for another pack format than its game reads, as after its version was
// changed: then while a snapshot held it, or before the panel restarted.
func (s *Server) stackOutdated(ctx context.Context, mc *avsysv1.MinecraftServer) bool {
	rp := mc.Spec.ResourcePack
	if rp == nil || len(rp.Stack) == 0 {
		return false
	}
	format, err := s.packFormatOf(ctx, controller.GameVersionOf(&mc.Spec))
	if err != nil || format == 0 {
		return false
	}
	return rp.Formats == nil || rp.Formats.Min != format || rp.Formats.Max != format
}

// restackFor joins a server's stack again for the game it runs now, if it
// is not made for it: the pack joined says the format of its game. Looked
// at once a minute, so one that cannot be now is later.
func (s *Server) restackFor(ctx context.Context, key client.ObjectKey) {
	// One at a time for each server.
	if _, busy := s.restacking.LoadOrStore(key.String(), true); busy {
		return
	}
	defer s.restacking.Delete(key.String())
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil || !s.stackOutdated(ctx, mc) {
		return
	}
	release, err := s.changing(mc, true)
	if err != nil {
		// Tried again in a minute.
		return
	}
	defer release()
	defer s.lockContent(key)()
	now, err := s.live(ctx, mc)
	if err != nil || !s.stackOutdated(ctx, now) {
		return
	}
	sc, done, err := s.filesOf(ctx, now, false)
	if err != nil {
		s.Log.Warn("could not join a server's resource packs again for its new version", "server", key.String(), "error", err)
		return
	}
	defer done()
	if _, err := s.restack(ctx, now, sc, now.Spec.ResourcePack.Stack); err != nil {
		s.Log.Warn("could not join a server's resource packs again for its new version", "server", key.String(), "error", err)
		return
	}
	go s.prunePacksNow(ctx)
}

// keptPacks are the SHA-1s of the packs whose files a server keeps for its
// resource pack: the one it offers, and those it is joined from.
func keptPacks(rp *avsysv1.ResourcePackSpec) []string {
	if rp == nil || rp.Uploaded == nil {
		return nil
	}
	out := []string{rp.SHA1}
	for _, p := range rp.Stack {
		out = append(out, p.SHA1)
	}
	return out
}

func inStack(rp *avsysv1.ResourcePackSpec, sha string) bool {
	for _, k := range keptPacks(rp) {
		if k == sha {
			return true
		}
	}
	return false
}

// stackedName is how a pack in a stack is spoken of.
func stackedName(p avsysv1.StackedPack) string {
	switch {
	case p.Project != nil && p.Project.Title != "":
		return p.Project.Title
	case p.Project != nil:
		return p.Project.ID
	case p.Name != "":
		return strings.TrimSuffix(p.Name, ".zip")
	}
	return p.SHA1[:8]
}

func (s *Server) packTemp(pattern string) (*os.File, error) {
	if err := os.MkdirAll(s.Config.PackDir, 0o750); err != nil {
		return nil, err
	}
	return os.CreateTemp(s.Config.PackDir, pattern)
}

// readPackFile copies a pack out of a server's files, with many reads in
// flight at once rather than one round trip at a time, and says its size
// and SHA-1. One over the limit is not read, and has no SHA-1.
func readPackFile(to io.Writer, f *sftp.File, limit int64) (int64, string, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	if info.Size() > limit {
		return info.Size(), "", nil
	}
	h := sha1.New()
	n, err := f.WriteTo(io.MultiWriter(to, h))
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// copyHashed copies up to limit+1 bytes, and says how many and their SHA-1.
func copyHashed(to io.Writer, from io.Reader, limit int64) (int64, string, error) {
	h := sha1.New()
	n, err := io.Copy(io.MultiWriter(to, h), io.LimitReader(from, limit+1))
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// downloadPack fetches a pack from a public address into to, and checks it
// has the SHA-1 it is said to, if one is.
func downloadPack(ctx context.Context, to io.Writer, address, want string, limit int64) (avsysv1.StackedPack, error) {
	target, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return avsysv1.StackedPack{}, errBadRequest("Enter an http:// or https:// URL.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return avsysv1.StackedPack{}, err
	}
	resp, err := publicHTTP.Do(req)
	if err != nil {
		return avsysv1.StackedPack{}, errBadRequest("Could not download %s.", target.Host)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return avsysv1.StackedPack{}, errBadRequest("%s answered %s.", target.Host, resp.Status)
	}
	size, sha, err := copyHashed(to, resp.Body, limit)
	if err != nil {
		return avsysv1.StackedPack{}, errBadRequest("The download was interrupted.")
	}
	if want != "" && !strings.EqualFold(strings.TrimSpace(want), sha) {
		return avsysv1.StackedPack{}, errBadRequest("The file at that address is not the one with the SHA-1 given.")
	}
	return avsysv1.StackedPack{SHA1: sha, Size: size, Name: path.Base(target.Path)}, nil
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
