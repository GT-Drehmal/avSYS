package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/mojang"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A player's head, as the game draws it in the player list: the face of
// their skin with its outer layer on top, 8 pixels square. It is made here
// from the skin Mojang has for the account, so that the pages show players
// without asking anyone but the panel.

// heads keeps the faces made, by UUID, for a while: skins change rarely.
type heads struct {
	mu    sync.Mutex
	faces map[string]headEntry
}

type headEntry struct {
	png     []byte // nil for an account with no skin of its own
	expires time.Time
}

const (
	headFor   = 6 * time.Hour
	headsKept = 4096
)

func (h *heads) get(uuid string) (headEntry, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.faces[uuid]
	if !ok || time.Now().After(e.expires) {
		return headEntry{}, false
	}
	return e, true
}

func (h *heads) put(uuid string, face []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.faces == nil || len(h.faces) >= headsKept {
		h.faces = map[string]headEntry{}
	}
	h.faces[uuid] = headEntry{png: face, expires: time.Now().Add(headFor)}
}

// getPlayerHead answers with a player's head as a PNG, or 404 for one with
// no skin of their own, whom the pages show by their initial.
func (s *Server) getPlayerHead(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	uuid := bareUUID(r.PathValue("uuid"))
	if uuid == "" {
		return errNotFound
	}
	e, ok := s.heads.get(uuid)
	if !ok {
		face, err := s.headOf(r.Context(), uuid)
		if err != nil {
			s.Log.Debug("could not make a player's head", "uuid", uuid, "error", err)
			return errNotFound
		}
		s.heads.put(uuid, face)
		e.png = face
	}
	if e.png == nil {
		return errNotFound
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=21600")
	_, err := w.Write(e.png)
	return err
}

// headOf makes a player's head from their skin: nil for one with none.
func (s *Server) headOf(ctx context.Context, uuid string) ([]byte, error) {
	var skin string
	if xuid := xuidOf(uuid); xuid != "" {
		// A Bedrock player's skin is the one GeyserMC made for Java of the
		// skin they last played in, if they have played through Geyser.
		body, err := s.catalog.get(ctx, geyserAPI+"skin/"+url.PathEscape(xuid), 6*time.Hour)
		if err != nil {
			return nil, err
		}
		if skin, err = geyserSkinURL(body); err != nil {
			return nil, err
		}
	} else {
		body, err := s.catalog.getOr(ctx, sessionProfileURL(uuid), 6*time.Hour, noContent)
		if err != nil {
			return nil, err
		}
		if skin, err = skinURL(body); err != nil {
			return nil, err
		}
	}
	if skin == "" {
		return nil, nil
	}
	// Mojang's texture server, asked again as Mojang is.
	var data []byte
	err := mojang.Do(ctx, func(ctx context.Context, _ int) (bool, error) {
		status, body, err := s.catalog.send(ctx, http.MethodGet, skin, nil)
		if err == nil && status != http.StatusOK {
			err = errNotFound
		}
		data = body
		return mojang.Again(status), err
	})
	if err != nil {
		return nil, err
	}
	return faceOf(data)
}

// skinURL reads where a profile's skin is: "" for none.
func skinURL(profile []byte) (string, error) {
	var p struct {
		Properties []struct{ Name, Value string }
	}
	if err := json.Unmarshal(profile, &p); err != nil {
		return "", err
	}
	for _, prop := range p.Properties {
		if prop.Name != "textures" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(prop.Value)
		if err != nil {
			return "", err
		}
		var t struct {
			Textures struct {
				Skin struct{ URL string } `json:"SKIN"`
			}
		}
		if err := json.Unmarshal(data, &t); err != nil {
			return "", err
		}
		// Only Mojang's own: the address is fetched by the panel.
		if u, err := url.Parse(t.Textures.Skin.URL); err == nil && u.Host == "textures.minecraft.net" {
			u.Scheme = "https"
			return u.String(), nil
		}
	}
	return "", nil
}

// textureID is the ID of a skin on Mojang's texture server.
var textureID = regexp.MustCompile(`^[0-9a-f]{1,128}$`)

// geyserSkinURL reads where the skin GeyserMC has for a Bedrock player is:
// "" for none, which it answers as {}.
func geyserSkinURL(body []byte) (string, error) {
	var skin struct {
		TextureID string `json:"texture_id"`
	}
	if err := json.Unmarshal(body, &skin); err != nil {
		return "", err
	}
	if !textureID.MatchString(skin.TextureID) {
		return "", nil
	}
	return "https://textures.minecraft.net/texture/" + skin.TextureID, nil
}

// faceOf cuts the face out of a skin, with the outer layer over it.
func faceOf(skin []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(skin))
	if err != nil {
		return nil, err
	}
	if b := img.Bounds(); b.Dx() < 64 || b.Dy() < 32 {
		return nil, errNotFound
	}
	at := img.Bounds().Min
	face := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(face, face.Bounds(), img, at.Add(image.Pt(8, 8)), draw.Src)
	draw.Draw(face, face.Bounds(), img, at.Add(image.Pt(40, 8)), draw.Over)
	var out bytes.Buffer
	if err := png.Encode(&out, face); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
