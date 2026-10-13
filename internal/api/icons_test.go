package api

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// picture is a w by h image, red on its left half and blue on its right.
func picture(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.NRGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.NRGBA{B: 255, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func asPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Whatever is uploaded comes out as the middle of it, square, at one size,
// as a PNG the panel drew.
func TestSquarePNG(t *testing.T) {
	read := func(data []byte) image.Image {
		t.Helper()
		out, err := squarePNG(bytes.NewReader(data), 128)
		if err != nil {
			t.Fatal(err)
		}
		img, format, err := image.Decode(bytes.NewReader(out))
		if err != nil || format != "png" {
			t.Fatalf("what came out is %s, %v; want a PNG", format, err)
		}
		return img
	}
	// Wider than tall: the middle square is kept, half red and half blue.
	img := read(asPNG(t, picture(600, 200)))
	if b := img.Bounds(); b.Dx() != 128 || b.Dy() != 128 {
		t.Fatalf("size %v, want 128 by 128", b)
	}
	left, right := color.NRGBAModel.Convert(img.At(10, 64)).(color.NRGBA), color.NRGBAModel.Convert(img.At(118, 64)).(color.NRGBA)
	if left.R < 250 || left.B > 5 || right.B < 250 || right.R > 5 {
		t.Errorf("left %v right %v, want red and blue", left, right)
	}
	// Smaller than asked for is scaled up, and a JPEG is as good as a PNG.
	if b := read(asPNG(t, picture(16, 16))).Bounds(); b.Dx() != 128 {
		t.Errorf("a small picture came out %v", b)
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, picture(300, 400), nil); err != nil {
		t.Fatal(err)
	}
	if b := read(jpg.Bytes()).Bounds(); b.Dx() != 128 || b.Dy() != 128 {
		t.Errorf("a JPEG came out %v", b)
	}
	// See-through stays see-through.
	clear := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	if _, _, _, a := read(asPNG(t, clear)).At(5, 5).RGBA(); a != 0 {
		t.Errorf("a see-through picture came out with alpha %d", a)
	}

	for name, data := range map[string][]byte{
		"not a picture":       []byte("<svg onload=alert(1)>"),
		"nothing":             nil,
		"a picture cut short": asPNG(t, picture(200, 200))[:60],
		// Small as a file, enormous as an image.
		"too many pixels": asPNG(t, picture(maxPictureSide+1, 1)),
	} {
		if _, err := squarePNG(bytes.NewReader(data), 128); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := squarePNG(bytes.NewReader(make([]byte, maxPictureBytes+1)), 128); err == nil || !strings.Contains(err.Error(), "4 MB") {
		t.Errorf("an upload over the limit: %v", err)
	}
}

// send makes a request with a body that is not text, and any extra headers.
func (p *panel) send(who, method, path string, body []byte, headers ...string) *httptest.ResponseRecorder {
	p.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

func TestAvatar(t *testing.T) {
	p := newPanel(t)
	id := p.users["operator"].ID
	avatarOf := func(who string) string {
		_, me := p.get(who, "GET", "/me", "")
		return me["user"].(map[string]any)["avatarUrl"].(string)
	}
	if rec := p.send("viewer", "GET", "/users/"+id+"/avatar", nil); rec.Code != 404 {
		t.Fatalf("a picture before any was set: %d", rec.Code)
	}
	if rec := p.send("operator", "PUT", "/me/avatar", []byte("not a picture")); rec.Code != 400 {
		t.Errorf("something that is not a picture: %d", rec.Code)
	}
	if rec := p.send("operator", "PUT", "/me/avatar", asPNG(t, picture(300, 300))); rec.Code != 204 {
		t.Fatalf("setting a picture: %d %s", rec.Code, rec.Body)
	}
	if got := avatarOf("operator"); got != "/api/v1/users/"+id+"/avatar" {
		t.Errorf("where the picture is: %q", got)
	}
	// Anyone signed in sees it; nobody else does.
	rec := p.send("viewer", "GET", "/users/"+id+"/avatar", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the picture: %d %v", rec.Code, rec.Header())
	}
	if img, _, err := image.Decode(rec.Body); err != nil || img.Bounds().Dx() != avatarSide {
		t.Errorf("what is served: %v, %v", img, err)
	}
	if got, _ := p.do("", "GET", "/users/"+id+"/avatar", ""); got != http.StatusUnauthorized {
		t.Errorf("signed out: %d, want 401", got)
	}
	// Asked for again unchanged, it is not sent again.
	tag := rec.Header().Get("ETag")
	if again := p.send("viewer", "GET", "/users/"+id+"/avatar", nil, "If-None-Match", tag); tag == "" || again.Code != 304 || again.Body.Len() != 0 {
		t.Errorf("asked for again with %q: %d, %d bytes", tag, again.Code, again.Body.Len())
	}
	if rec := p.send("operator", "DELETE", "/me/avatar", nil); rec.Code != 204 || avatarOf("operator") != "" {
		t.Errorf("removing it: %d, then %q", rec.Code, avatarOf("operator"))
	}

	// A picture that came with a sign-in from elsewhere is not the panel's
	// to take away.
	const theirs = "https://example.com/a.png"
	if err := p.srv.Store.SetAvatarURL(context.Background(), p.users["admin"].ID, theirs); err != nil {
		t.Fatal(err)
	}
	if rec := p.send("admin", "DELETE", "/me/avatar", nil); rec.Code != 204 || avatarOf("admin") != theirs {
		t.Errorf("removing a picture that was never uploaded: %d, then %q", rec.Code, avatarOf("admin"))
	}
	// One uploaded over it stands in for it, and it comes back when that
	// one is removed.
	if rec := p.send("admin", "PUT", "/me/avatar", asPNG(t, picture(64, 64))); rec.Code != 204 || avatarOf("admin") == theirs {
		t.Errorf("uploading over it: %d, then %q", rec.Code, avatarOf("admin"))
	}
	if rec := p.send("admin", "PUT", "/me/avatar", asPNG(t, picture(32, 32))); rec.Code != 204 {
		t.Errorf("uploading a second time: %d", rec.Code)
	}
	if rec := p.send("admin", "DELETE", "/me/avatar", nil); rec.Code != 204 || avatarOf("admin") != theirs {
		t.Errorf("removing the upload: %d, then %q, want the one from before", rec.Code, avatarOf("admin"))
	}
	if _, err := p.srv.Store.Setting(context.Background(), providerAvatarKey(p.users["admin"].ID)); err != store.ErrNotFound {
		t.Errorf("the address kept aside was left behind: %v", err)
	}
}

func TestOrgIcon(t *testing.T) {
	p := newPanel(t)
	hasIcon := func(who string) any {
		_, org := p.get(who, "GET", "/orgs/alpha", "")
		return org["hasIcon"]
	}
	if hasIcon("viewer") != false {
		t.Fatalf("an organization starts with a picture: %v", hasIcon("viewer"))
	}
	if rec := p.send("operator", "PUT", "/orgs/alpha/icon", asPNG(t, picture(64, 64))); rec.Code != 403 {
		t.Errorf("an operator setting it: %d, want 403", rec.Code)
	}
	// Like its name, it is its owners' to change.
	if rec := p.send("admin", "PUT", "/orgs/alpha/icon", asPNG(t, picture(64, 64))); rec.Code != 403 {
		t.Errorf("an admin setting it: %d, want 403", rec.Code)
	}
	if rec := p.send("owner", "PUT", "/orgs/alpha/icon", asPNG(t, picture(64, 64))); rec.Code != 204 {
		t.Fatalf("an owner setting it: %d %s", rec.Code, rec.Body)
	}
	if hasIcon("viewer") != true {
		t.Error("the organization does not say it has a picture")
	}
	if rec := p.send("viewer", "GET", "/orgs/alpha/icon", nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("a member fetching it: %d", rec.Code)
	}
	if rec := p.send("outsider", "GET", "/orgs/alpha/icon", nil); rec.Code != 404 {
		t.Errorf("someone outside fetching it: %d, want 404", rec.Code)
	}
	// It is in the list of one's organizations too, which is what draws them.
	_, me := p.get("viewer", "GET", "/me", "")
	if orgs := me["orgs"].([]any); len(orgs) != 1 || orgs[0].(map[string]any)["hasIcon"] != true {
		t.Errorf("the organization as listed: %v", me["orgs"])
	}
	// It goes with the organization.
	beta, err := p.srv.Store.OrgBySlug(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if rec := p.send("outsider", "PUT", "/orgs/beta/icon", asPNG(t, picture(64, 64))); rec.Code != 204 {
		t.Fatalf("setting beta's: %d", rec.Code)
	}
	if code, _ := p.do("outsider", "DELETE", "/orgs/beta", ""); code != 204 {
		t.Fatalf("deleting beta: %d", code)
	}
	if _, _, err := p.srv.Store.Image(context.Background(), store.OrgImage(beta.ID)); err != store.ErrNotFound {
		t.Errorf("a deleted organization's picture was left behind: %v", err)
	}
	if rec := p.send("owner", "DELETE", "/orgs/alpha/icon", nil); rec.Code != 204 || hasIcon("viewer") != false {
		t.Errorf("removing it: %d, then hasIcon %v", rec.Code, hasIcon("viewer"))
	}
}

// withOrientation gives a JPEG the EXIF note a camera writes to say which
// way up the photo is.
func withOrientation(jpg []byte, turn byte) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, turn, 0, 0, 0, 0, 0, 0}
	exif := append([]byte("Exif\x00\x00"), tiff...)
	seg := append([]byte{0xFF, 0xE1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}, exif...)
	return append(append(append([]byte{}, jpg[:2]...), seg...), jpg[2:]...)
}

// A photo is stored as the camera's sensor saw it, with a note of which way
// is up. What comes out is the right way up.
func TestSquarePNGTurnsPhotosTheRightWayUp(t *testing.T) {
	var plain bytes.Buffer
	// Red on the left, blue on the right, as stored.
	if err := jpeg.Encode(&plain, picture(400, 400), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	if got := jpegOrientation(plain.Bytes()); got != 1 {
		t.Errorf("a JPEG that does not say: %d, want 1", got)
	}
	red := func(c color.Color) bool { n := color.NRGBAModel.Convert(c).(color.NRGBA); return n.R > 200 && n.B < 60 }
	for turn, where := range map[byte]string{1: "left", 2: "right", 3: "right", 4: "left", 5: "top", 6: "top", 7: "bottom", 8: "bottom"} {
		data := withOrientation(plain.Bytes(), turn)
		if got := jpegOrientation(data); got != int(turn) {
			t.Errorf("orientation read back: %d, want %d", got, turn)
			continue
		}
		out, err := squarePNG(bytes.NewReader(data), 64)
		if err != nil {
			t.Fatalf("orientation %d: %v", turn, err)
		}
		img, _, _ := image.Decode(bytes.NewReader(out))
		// The four corners say which half is red: top left, top right,
		// bottom left, bottom right.
		got := [4]bool{red(img.At(16, 16)), red(img.At(48, 16)), red(img.At(16, 48)), red(img.At(48, 48))}
		want := map[string][4]bool{
			"left": {true, false, true, false}, "right": {false, true, false, true},
			"top": {true, true, false, false}, "bottom": {false, false, true, true},
		}[where]
		if got != want {
			t.Errorf("orientation %d: red corners %v, want red on the %s %v", turn, got, where, want)
		}
	}
	// A note that is cut short or says nonsense is no note.
	for name, data := range map[string][]byte{
		"cut short":  withOrientation(plain.Bytes(), 6)[:20],
		"nonsense":   withOrientation(plain.Bytes(), 9),
		"not a JPEG": []byte("hello"),
	} {
		if got := jpegOrientation(data); got != 1 {
			t.Errorf("%s: %d, want 1", name, got)
		}
	}
}
