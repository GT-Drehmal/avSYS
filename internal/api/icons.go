package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	_ "image/gif" // pictures may be given as GIF, JPEG or PNG
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/pkg/sftp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

const (
	// maxPictureBytes and maxPictureSide bound what is accepted to be made
	// into a picture. The side is checked before anything is decoded: a
	// small file can claim to be an enormous image.
	maxPictureBytes = 4 << 20
	maxPictureSide  = 4096
	// avatarSide is the side of an organization's or a person's picture.
	avatarSide = 128
	// serverIconSide is the only size the game shows for a server.
	serverIconSide = 64
	// newServerIconWait is how long a picture waits for a new server's
	// volume. Provisioning one and pulling the workspace image on a node
	// that has never run one is the slow case.
	newServerIconWait = 3 * time.Minute
	// serverIconPath is where the game looks for it.
	serverIconPath = dataRoot + "/server-icon.png"
)

var errNotAPicture = errBadRequest("That is not a picture the panel can read. Use a PNG, JPEG or GIF of up to 4 MB.")

// decoding lets a few pictures be unpacked at a time. One of the largest
// accepted takes some tens of megabytes while it is, and anyone signed in
// can send one.
var decoding = make(chan struct{}, 2)

// squarePNG turns an uploaded picture into a square PNG of the given side:
// the middle of it, scaled, the right way up. Whatever was uploaded is
// decoded and drawn anew, so what is stored and served is always an image
// made here, never the bytes someone sent.
func squarePNG(r io.Reader, side int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxPictureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPictureBytes {
		return nil, &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "Pictures are up to 4 MB."}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, errNotAPicture
	}
	if cfg.Width > maxPictureSide || cfg.Height > maxPictureSide {
		return nil, errBadRequest("Pictures are up to %d pixels a side.", maxPictureSide)
	}
	decoding <- struct{}{}
	defer func() { <-decoding }()
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errNotAPicture
	}
	// A camera stores a photo as its sensor saw it and says separately
	// which way is up. Decoding gives the former.
	turn := 1
	if format == "jpeg" {
		turn = jpegOrientation(raw)
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if turn >= 5 {
		// Those are the ones on their side.
		w, h = h, w
	}
	// at reads the pixel at (x, y) of the picture the right way up.
	at := func(x, y int) color.NRGBA {
		sx, sy := x, y
		switch turn {
		case 2:
			sx = w - 1 - x
		case 3:
			sx, sy = w-1-x, h-1-y
		case 4:
			sy = h - 1 - y
		case 5:
			sx, sy = y, x
		case 6:
			sx, sy = y, w-1-x
		case 7:
			sx, sy = h-1-y, w-1-x
		case 8:
			sx, sy = h-1-y, x
		}
		return color.NRGBAModel.Convert(src.At(bounds.Min.X+sx, bounds.Min.Y+sy)).(color.NRGBA)
	}

	// The largest square in the middle.
	crop := min(w, h)
	x0, y0 := (w-crop)/2, (h-crop)/2
	dst := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := range side {
		for x := range side {
			// Each pixel is the average of points spread over the block of
			// the original it covers: all of them where the block is small,
			// a grid of them where it is large. The original is read where
			// it lies and never copied whole.
			bx0, bx1 := x0+x*crop/side, x0+(x+1)*crop/side
			by0, by1 := y0+y*crop/side, y0+(y+1)*crop/side
			nx, ny := min(max(bx1-bx0, 1), samplesPerSide), min(max(by1-by0, 1), samplesPerSide)
			var r, g, b, a, n uint32
			for j := range ny {
				for i := range nx {
					c := at(bx0+(2*i+1)*max(bx1-bx0, 1)/(2*nx), by0+(2*j+1)*max(by1-by0, 1)/(2*ny))
					alpha := uint32(c.A)
					// Weighted by how solid each point is, so that
					// see-through ones do not darken an edge.
					r += uint32(c.R) * alpha
					g += uint32(c.G) * alpha
					b += uint32(c.B) * alpha
					a += alpha
					n++
				}
			}
			i := dst.PixOffset(x, y)
			if a > 0 {
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2] = uint8(r/a), uint8(g/a), uint8(b/a)
			}
			dst.Pix[i+3] = uint8(a / n)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// samplesPerSide is how many points across and down are read of the part
// of the original that becomes one pixel.
const samplesPerSide = 4

// jpegOrientation reads which way up a JPEG says it is: 1 to 8 as EXIF
// numbers them, and 1, as stored, when it does not say or cannot be read.
func jpegOrientation(raw []byte) int {
	// Segments follow the two-byte start marker, each a marker, a length
	// that counts itself, and that much data.
	for i := 2; i+4 <= len(raw) && raw[i] == 0xFF; {
		marker, size := raw[i+1], int(raw[i+2])<<8|int(raw[i+3])
		if size < 2 || i+2+size > len(raw) {
			return 1
		}
		seg := raw[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) >= 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		if marker == 0xDA {
			// The picture itself begins: no more headers.
			return 1
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	dir := int(order.Uint32(tiff[4:8]))
	if dir < 8 || dir+2 > len(tiff) {
		return 1
	}
	entries := int(order.Uint16(tiff[dir : dir+2]))
	for e := range entries {
		at := dir + 2 + e*12
		if at+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[at:at+2]) == 0x0112 {
			if v := int(order.Uint16(tiff[at+8 : at+10])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// servePicture answers with a PNG the panel made. It is revalidated on every
// use and costs nothing when it has not changed, so that a new picture shows
// at once without its address having to change.
func servePicture(w http.ResponseWriter, r *http.Request, data []byte) {
	sum := sha1.Sum(data)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", tag)
	// Kept by the browser for half a minute, then checked again against the
	// tag. One changed here is asked for under a new address straight away.
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(data)
}

func (s *Server) serveStored(w http.ResponseWriter, r *http.Request, key string) error {
	data, _, err := s.Store.Image(r.Context(), key)
	if err != nil {
		return err
	}
	servePicture(w, r, data)
	return nil
}

func (s *Server) getOrgIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	return s.serveStored(w, r, store.OrgImage(a.Org.ID))
}

// putOrgIcon sets the organization's picture. Like its name, that is its
// owners' to change.
func (s *Server) putOrgIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOwner)
	if err != nil {
		return err
	}
	data, err := squarePNG(r.Body, avatarSide)
	if err != nil {
		return err
	}
	if err := s.Store.SetImage(r.Context(), store.OrgImage(a.Org.ID), data); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "org.icon", a.Org.Slug, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) deleteOrgIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOwner)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteImage(r.Context(), store.OrgImage(a.Org.ID)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// avatarURL is where a user's uploaded picture is served from.
func avatarURL(userID string) string { return "/api/v1/users/" + userID + "/avatar" }

// getAvatar serves a user's uploaded picture to anyone signed in: it is
// shown wherever that person is, to whoever can see them.
func (s *Server) getAvatar(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	return s.serveStored(w, r, store.UserImage(r.PathValue("user")))
}

// providerAvatarKey is where the address of the picture a user had from
// signing in elsewhere is kept while one they uploaded stands in for it.
func providerAvatarKey(userID string) string { return "provider-avatar/" + userID }

func (s *Server) putAvatar(w http.ResponseWriter, r *http.Request, u *store.User) error {
	data, err := squarePNG(r.Body, avatarSide)
	if err != nil {
		return err
	}
	// Kept aside, to go back to when the upload is removed.
	if u.AvatarURL != "" && u.AvatarURL != avatarURL(u.ID) {
		if err := s.Store.SetSetting(r.Context(), providerAvatarKey(u.ID), u.AvatarURL); err != nil {
			return err
		}
	}
	if err := s.Store.SetImage(r.Context(), store.UserImage(u.ID), data); err != nil {
		return err
	}
	if err := s.Store.SetAvatarURL(r.Context(), u.ID, avatarURL(u.ID)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) deleteAvatar(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if err := s.Store.DeleteImage(r.Context(), store.UserImage(u.ID)); err != nil {
		return err
	}
	// Only a picture that was uploaded is taken away. One that came with a
	// sign-in from elsewhere is that account's: it stays, or comes back.
	if u.AvatarURL == avatarURL(u.ID) {
		before, err := s.Store.Setting(r.Context(), providerAvatarKey(u.ID))
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := s.Store.SetAvatarURL(r.Context(), u.ID, before); err != nil {
			return err
		}
		if err := s.Store.DeleteSetting(r.Context(), providerAvatarKey(u.ID)); err != nil {
			return err
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// serverIcons remembers each server's icon for a little while. Reading one
// means reaching into the server's workspace, a list of servers asks for all
// of them at once, and the game's server list asks without being signed in.
type serverIcons struct {
	mu    sync.Mutex
	icons map[string]serverIcon
	// reading holds, for each server whose icon is being read right now, a
	// channel that is closed when that read is over. Whoever else wants it
	// waits for that one read.
	reading map[string]chan struct{}
	// changes counts how often each server's icon was set or removed here.
	// A read that began before a change knows nothing of it and is not
	// kept.
	changes map[string]int
}

type serverIcon struct {
	// data is nil for a server that has no icon.
	data []byte
	at   time.Time
	// failed marks one that could not be read, which is asked again sooner.
	failed bool
}

const (
	serverIconFor = 2 * time.Minute
	// serverIconRetry is how long a server whose icon could not be read is
	// left alone before it is tried again.
	serverIconRetry = 20 * time.Second
	// serverIconRead bounds one read.
	serverIconRead = 15 * time.Second
)

func (c *serverIcons) init() {
	if c.icons == nil {
		c.icons, c.reading, c.changes = map[string]serverIcon{}, map[string]chan struct{}{}, map[string]int{}
	}
}

func (c *serverIcons) fresh(key string) (serverIcon, bool) {
	icon, ok := c.icons[key]
	keep := serverIconFor
	if icon.failed {
		keep = serverIconRetry
	}
	return icon, ok && time.Since(icon.at) < keep
}

func (c *serverIcons) get(key string) (serverIcon, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fresh(key)
}

// set records an icon that was just written, in place of whatever a read
// under way may come back with.
func (c *serverIcons) set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.changes[key]++
	c.icons[key] = serverIcon{data: data, at: time.Now()}
}

// forget drops what is remembered of a server, after its icon was removed
// or the server was.
func (c *serverIcons) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.init()
	c.changes[key]++
	delete(c.icons, key)
}

// load returns a server's icon, reading it with read if it is not
// remembered. However many ask at once, one read is made; the rest wait for
// it, or stop waiting when their own ctx ends. ok is false when it could
// not be read.
//
// One remembered for longer than it is fresh for is still answered at once,
// and read again behind the answer: a page of servers shows their pictures
// without waiting on their workspaces, and is at most one read behind.
func (c *serverIcons) load(ctx context.Context, key string, read func(context.Context) ([]byte, error)) (data []byte, ok bool) {
	for {
		c.mu.Lock()
		c.init()
		if icon, fresh := c.fresh(key); fresh {
			c.mu.Unlock()
			return icon.data, !icon.failed
		}
		stale, known := c.icons[key]
		wait, busy := c.reading[key]
		if !busy {
			wait = make(chan struct{})
			c.reading[key] = wait
			seen := c.changes[key]
			c.mu.Unlock()
			// On its own, so that it is seen through and remembered even
			// if nobody waits for it, and bounded, so that a workspace that
			// does not answer cannot hold it for ever.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), serverIconRead)
				defer cancel()
				data, err := read(ctx)
				c.mu.Lock()
				defer c.mu.Unlock()
				// Not kept if the icon was set or removed meanwhile: this
				// is what was there before. A failed read does not replace
				// one that was read before: that one is still the best known.
				if c.changes[key] == seen {
					if err == nil || !known || stale.failed {
						c.icons[key] = serverIcon{data: data, at: time.Now(), failed: err != nil}
					} else {
						c.icons[key] = serverIcon{data: stale.data, at: time.Now()}
					}
				}
				delete(c.reading, key)
				close(wait)
			}()
		} else {
			c.mu.Unlock()
		}
		// What was read before is the answer while it is read again.
		if known && !stale.failed {
			return stale.data, true
		}
		select {
		case <-wait:
			// Round again for what it found, or for what was set meanwhile.
			c.mu.Lock()
			icon, fresh := c.fresh(key)
			c.mu.Unlock()
			if fresh {
				return icon.data, !icon.failed
			}
			// Changed while it was read, and not remembered: read again.
		case <-ctx.Done():
			return nil, false
		}
	}
}

// warm reads the icons of servers that are not remembered yet, a few at a
// time, so that a page that lists them finds them ready when it asks.
func (s *Server) warmIcons(namespace string, names []string) {
	slots := make(chan struct{}, 4)
	for _, name := range names {
		s.serverIcons.mu.Lock()
		_, known := s.serverIcons.icons[namespace+"/"+name]
		s.serverIcons.mu.Unlock()
		if known {
			continue
		}
		slots <- struct{}{}
		go func() {
			defer func() { <-slots }()
			ctx, cancel := context.WithTimeout(context.Background(), serverIconRead)
			defer cancel()
			s.ServerIcon(ctx, namespace, name)
		}()
	}
}

// readServerIcon reads server-icon.png out of a server's files and makes
// the panel's own PNG of it. nil means the server has none, or none that is
// a picture.
func (s *Server) readServerIcon(ctx context.Context, namespace, name string) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		sc, err := s.workspaces.get(ctx, namespace, name)
		if err != nil {
			done <- result{err: err}
			return
		}
		f, err := sc.Open(serverIconPath)
		if errors.Is(err, fs.ErrNotExist) {
			done <- result{}
			return
		}
		if err != nil {
			done <- result{err: err}
			return
		}
		defer f.Close()
		// The file is the server's, which anyone with a shell there can put
		// anything into. It is redrawn like any upload before it is served.
		data, err := squarePNG(f, serverIconSide)
		if err != nil {
			// Not a picture: the same as none.
			done <- result{}
			return
		}
		done <- result{data: data}
	}()
	// Reading a file there cannot be called off, so it is not waited for
	// past ctx. serverIcons makes one such read per server at a time, which
	// is the most that can be left hanging on a workspace that has stalled.
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ServerIcon returns a server's icon as a 64 pixel PNG, or nil if it has
// none or its files cannot be reached just now. It is what the game's own
// server list is given while the game is not running to give it.
func (s *Server) ServerIcon(ctx context.Context, namespace, name string) []byte {
	data, _ := s.serverIcons.load(ctx, namespace+"/"+name, func(ctx context.Context) ([]byte, error) {
		return s.readServerIcon(ctx, namespace, name)
	})
	return data
}

// snapshotIcons remembers the icons read out of snapshots. A snapshot never
// changes, so what was read of one is good for as long as it is kept; a
// page that offers several snapshots asks for each.
type snapshotIcons struct {
	mu    sync.Mutex
	icons map[string][]byte
}

// snapshotIconsKept bounds how many are remembered. They are 64 pixel PNGs.
const snapshotIconsKept = 512

func (c *snapshotIcons) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.icons[key]
	return data, ok
}

func (c *snapshotIcons) set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.icons == nil || len(c.icons) >= snapshotIconsKept {
		c.icons = map[string][]byte{}
	}
	c.icons[key] = data
}

// snapshotIconLimit is the most read of a server-icon.png in a snapshot. The
// game's own is a few kilobytes; the file is the server's, and could be
// anything.
const snapshotIconLimit = 1 << 20

// getSnapshotIcon serves the icon a snapshot holds: the picture a server
// made from it will have.
func (s *Server) getSnapshotIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	snap, err := s.Backups.Get(r.Context(), a.Org.Slug, r.PathValue("id"))
	if err != nil {
		return snapshotError(err)
	}
	key := a.Org.Slug + "/" + snap.ID
	data, ok := s.snapshotIcons.get(key)
	if !ok {
		raw, err := s.Backups.File(r.Context(), a.Org.Slug, snap.ID, serverIconPath, snapshotIconLimit)
		switch {
		case errors.Is(err, backup.ErrTooLarge):
			// Not a picture the game would show: the same as none.
		case err != nil:
			return err
		case raw != nil:
			// Redrawn like any upload before it is served. Not a picture is
			// the same as none.
			data, _ = squarePNG(bytes.NewReader(raw), serverIconSide)
		}
		s.snapshotIcons.set(key, data)
	}
	if data == nil {
		return errNotFound
	}
	servePicture(w, r, data)
	return nil
}

// getServerIcon serves the icon the game shows for a server in its
// multiplayer list.
func (s *Server) getServerIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	// One that cannot be read just now is not worth more than a missing
	// picture, and is asked again soon.
	data := s.ServerIcon(r.Context(), a.Namespace, mc.Name)
	if data == nil {
		return errNotFound
	}
	servePicture(w, r, data)
	return nil
}

// putServerIcon writes an uploaded picture as the server's icon, at the
// size the game wants. The game reads it when it starts.
//
// With ?wait=true it is for a server made a moment ago, which has nowhere
// to put a picture until its volume is there: that is waited for. With
// &start=true the server is then started. That pair is how a picture chosen
// while making a server reaches it before the game first looks: the server
// is made stopped, given its picture, and started, here and not by the page,
// so that it happens whether or not the page is still open. A server is
// never left stopped over a picture: it is started even if the picture
// could not be put in place.
func (s *Server) putServerIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	q := r.URL.Query()
	wait := q.Get("wait") == "true"
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if wait && apierrors.IsNotFound(err) {
		// Asked for straight after it was made, before the panel's own view
		// of the cluster has caught up with it.
		if a, err = s.org(r, u, store.RoleAdmin); err != nil {
			return err
		}
		mc = &avsysv1.MinecraftServer{}
		err = s.Reader.Get(r.Context(), client.ObjectKey{Namespace: a.Namespace, Name: r.PathValue("server")}, mc)
	}
	if err != nil {
		return err
	}
	release, err := s.changing(mc, false)
	if err != nil {
		return err
	}
	defer release()
	data, err := squarePNG(r.Body, serverIconSide)
	if err != nil {
		return err
	}
	key := a.Namespace + "/" + mc.Name
	if !wait {
		sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
		if err != nil {
			return err
		}
		if err := writeServerIcon(sc, data); err != nil {
			return err
		}
	} else {
		// From here on it no longer matters whether whoever asked is still
		// there: the picture is in hand, and the server is waiting on this.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), newServerIconWait)
		defer cancel()
		placed := s.placeNewServerIcon(ctx, mc, data)
		if q.Get("start") == "true" {
			if err := s.startAfterIcon(ctx, mc); err != nil {
				return err
			}
		}
		if placed != nil {
			s.Log.Warn("a new server's picture was not put in place", "server", key, "error", placed)
			return &Error{Status: http.StatusGatewayTimeout, Code: "not_ready", Message: "The server's storage was not ready in time to take its picture. Set it in the server's settings."}
		}
	}
	s.serverIcons.set(key, data)
	s.audit(context.WithoutCancel(r.Context()), u, a.Org, "server.icon", mc.Name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// writeServerIcon puts a picture where the game looks for it. It is written
// beside its place and moved in, so that the game never finds half an icon.
func writeServerIcon(sc *sftp.Client, data []byte) error {
	tmp := serverIconPath + ".avsys-upload"
	f, err := sc.Create(tmp)
	if err != nil {
		return fileError(err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sc.PosixRename(tmp, serverIconPath)
	}
	if err != nil {
		_ = sc.Remove(tmp)
		return fileError(err)
	}
	return nil
}

// placeNewServerIcon waits for a new server's volume and puts its picture
// there. It goes by the pod made for this server and a connection of its
// own: a server made under the name of one just deleted finds the old one's
// pod, and a connection kept to it, still there for a while.
func (s *Server) placeNewServerIcon(ctx context.Context, mc *avsysv1.MinecraftServer, data []byte) error {
	if err := s.ownPod(ctx, mc); err != nil {
		return err
	}
	ws, err := s.dialWorkspaceWithin(ctx, mc.Namespace, mc.Name, newServerIconWait)
	if err != nil {
		return err
	}
	defer ws.Close()
	sc, err := sftp.NewClient(ws)
	if err != nil {
		return err
	}
	defer sc.Close()
	return writeServerIcon(sc, data)
}

// startAfterIcon starts a server that was made stopped to be given its
// picture first.
func (s *Server) startAfterIcon(ctx context.Context, mc *avsysv1.MinecraftServer) error {
	now := &avsysv1.MinecraftServer{}
	if err := s.Reader.Get(ctx, client.ObjectKeyFromObject(mc), now); err != nil {
		return err
	}
	if now.Spec.State == avsysv1.StateRunning {
		return nil
	}
	orig := now.DeepCopy()
	now.Spec.State = avsysv1.StateRunning
	return s.Client.Patch(ctx, now, client.MergeFrom(orig))
}

func (s *Server) deleteServerIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, false)
	if err != nil {
		return err
	}
	defer release()
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		return err
	}
	if err := sc.Remove(serverIconPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileError(err)
	}
	s.serverIcons.forget(a.Namespace + "/" + mc.Name)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
