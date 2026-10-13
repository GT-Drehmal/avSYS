package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"image"
	"image/draw"
	_ "image/gif" // pictures may be given as GIF, JPEG or PNG
	_ "image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"time"

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
	// serverIconPath is where the game looks for it.
	serverIconPath = dataRoot + "/server-icon.png"
)

var errNotAPicture = errBadRequest("That is not a picture the panel can read. Use a PNG, JPEG or GIF of up to 4 MB.")

// squarePNG turns an uploaded picture into a square PNG of the given side:
// the middle of it, scaled. Whatever was uploaded is decoded and drawn anew,
// so what is stored and served is always an image made here, never the
// bytes someone sent.
func squarePNG(r io.Reader, side int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxPictureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPictureBytes {
		return nil, &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "Pictures are up to 4 MB."}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, errNotAPicture
	}
	if cfg.Width > maxPictureSide || cfg.Height > maxPictureSide {
		return nil, errBadRequest("Pictures are up to %d pixels a side.", maxPictureSide)
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errNotAPicture
	}
	// Into a known layout first, so that scaling reads plain bytes.
	bounds := decoded.Bounds()
	src := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(src, src.Bounds(), decoded, bounds.Min, draw.Src)

	// The largest square in the middle.
	crop := min(bounds.Dx(), bounds.Dy())
	x0, y0 := (bounds.Dx()-crop)/2, (bounds.Dy()-crop)/2
	dst := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := range side {
		for x := range side {
			// Each pixel is the average of the block of the original it
			// covers, which is at least one pixel of it.
			sx0, sx1 := x0+x*crop/side, x0+(x+1)*crop/side
			sy0, sy1 := y0+y*crop/side, y0+(y+1)*crop/side
			sx1, sy1 = max(sx1, sx0+1), max(sy1, sy0+1)
			var r, g, b, a, n uint32
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					i := src.PixOffset(sx, sy)
					alpha := uint32(src.Pix[i+3])
					// Weighted by how solid each pixel is, so that
					// see-through ones do not darken an edge.
					r += uint32(src.Pix[i]) * alpha
					g += uint32(src.Pix[i+1]) * alpha
					b += uint32(src.Pix[i+2]) * alpha
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

// servePicture answers with a PNG the panel made. It is revalidated on every
// use and costs nothing when it has not changed, so that a new picture shows
// at once without its address having to change.
func servePicture(w http.ResponseWriter, r *http.Request, data []byte) {
	sum := sha1.Sum(data)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "private, no-cache")
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

func (s *Server) putOrgIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
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
	a, err := s.org(r, u, store.RoleAdmin)
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

func (s *Server) putAvatar(w http.ResponseWriter, r *http.Request, u *store.User) error {
	data, err := squarePNG(r.Body, avatarSide)
	if err != nil {
		return err
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
	// sign-in from elsewhere is that account's, and stays.
	if u.AvatarURL == avatarURL(u.ID) {
		if err := s.Store.SetAvatarURL(r.Context(), u.ID, ""); err != nil {
			return err
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// serverIcons remembers each server's icon for a little while. Reading one
// means reaching into the server's workspace, and a list of servers asks
// for all of them at once.
type serverIcons struct {
	mu    sync.Mutex
	icons map[string]serverIcon
}

type serverIcon struct {
	// data is nil for a server that has no icon.
	data []byte
	at   time.Time
}

const serverIconFor = 2 * time.Minute

func (c *serverIcons) get(key string) (serverIcon, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	icon, ok := c.icons[key]
	return icon, ok && time.Since(icon.at) < serverIconFor
}

func (c *serverIcons) set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.icons == nil {
		c.icons = map[string]serverIcon{}
	}
	c.icons[key] = serverIcon{data: data, at: time.Now()}
}

func (c *serverIcons) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.icons, key)
}

// readServerIcon reads server-icon.png out of a server's files and makes
// the panel's own PNG of it. nil means the server has none, or none that is
// a picture.
func (s *Server) readServerIcon(ctx context.Context, namespace, name string) ([]byte, error) {
	sc, err := s.workspaces.get(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	f, err := sc.Open(serverIconPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The file is the server's, which anyone with a shell there can put
	// anything into. It is redrawn like any upload before it is served.
	data, err := squarePNG(f, serverIconSide)
	if err != nil {
		return nil, nil
	}
	return data, nil
}

// getServerIcon serves the icon the game shows for a server in its
// multiplayer list.
func (s *Server) getServerIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	key := a.Namespace + "/" + mc.Name
	icon, fresh := s.serverIcons.get(key)
	if !fresh {
		data, err := s.readServerIcon(r.Context(), a.Namespace, mc.Name)
		if err != nil {
			// Its workspace is not reachable just now. That is not worth
			// more than a missing picture, and is asked again soon.
			return errNotFound
		}
		s.serverIcons.set(key, data)
		icon.data = data
	}
	if icon.data == nil {
		return errNotFound
	}
	servePicture(w, r, icon.data)
	return nil
}

// putServerIcon writes an uploaded picture as the server's icon, at the
// size the game wants. The game reads it when it starts.
func (s *Server) putServerIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
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
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		return err
	}
	// Written beside it and moved into place, so that the game never finds
	// half an icon.
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
	s.serverIcons.set(a.Namespace+"/"+mc.Name, data)
	s.audit(r.Context(), u, a.Org, "server.icon", mc.Name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
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
