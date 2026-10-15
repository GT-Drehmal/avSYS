package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// A server's icon is remembered for a while, and not past the server: one
// made later under the same name is another server.
func TestDeletingAServerForgetsItsIcon(t *testing.T) {
	p := newPanel(t)
	p.srv.serverIcons.set("mc-alpha/world", []byte("an icon"))
	if _, fresh := p.srv.serverIcons.get("mc-alpha/world"); !fresh {
		t.Fatal("the icon was not remembered")
	}
	if code, name := p.do("admin", "DELETE", "/orgs/alpha/servers/world?snapshot=false", ""); code != 204 {
		t.Fatalf("deleting the server: %d %s", code, name)
	}
	if _, fresh := p.srv.serverIcons.get("mc-alpha/world"); fresh {
		t.Error("a deleted server's icon is still remembered")
	}
}

// However many ask for a server's icon at once, it is read once; a read
// that fails is not repeated straight away; and one that began before the
// icon was changed does not put the old one back.
func TestServerIconsLoad(t *testing.T) {
	ctx := t.Context()
	var c serverIcons
	var reads atomic.Int32
	release := make(chan struct{})
	slow := func(context.Context) ([]byte, error) {
		reads.Add(1)
		<-release
		return []byte("old"), nil
	}

	// Many at once: one read.
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, _ := c.load(ctx, "ns/world", slow)
			results[i] = string(data)
		}()
	}
	for reads.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Errorf("%d reads for eight asking at once, want 1", n)
	}
	for i, got := range results {
		if got != "old" {
			t.Errorf("asker %d got %q", i, got)
		}
	}
	// Remembered: no read at all.
	if data, ok := c.load(ctx, "ns/world", func(context.Context) ([]byte, error) { t.Error("read again"); return nil, nil }); !ok || string(data) != "old" {
		t.Errorf("asked again: %q %v", data, ok)
	}

	// A read that fails is remembered as that for a little while.
	failing := 0
	fail := func(context.Context) ([]byte, error) { failing++; return nil, errors.New("workspace unreachable") }
	if data, ok := c.load(ctx, "ns/down", fail); ok || data != nil {
		t.Errorf("a failed read: %q %v", data, ok)
	}
	if _, ok := c.load(ctx, "ns/down", fail); ok || failing != 1 {
		t.Errorf("asked again straight after a failure: ok %v after %d reads, want no second read", ok, failing)
	}

	// Set while an older read is under way: the read's answer is not kept.
	gate := make(chan struct{})
	started := make(chan struct{})
	got := make(chan string, 1)
	go func() {
		data, _ := c.load(ctx, "ns/changing", func(context.Context) ([]byte, error) {
			close(started)
			<-gate
			return []byte("before"), nil
		})
		got <- string(data)
	}()
	<-started
	c.set("ns/changing", []byte("after"))
	close(gate)
	if v := <-got; v != "after" {
		t.Errorf("the asker waiting through a change got %q, want what was set", v)
	}
	if icon, _ := c.get("ns/changing"); string(icon.data) != "after" {
		t.Errorf("remembered after the old read finished: %q, want what was set", icon.data)
	}

	// Removed while a read is under way: what it found is not put back.
	gate, started = make(chan struct{}), make(chan struct{})
	calls := 0
	go func() {
		data, _ := c.load(ctx, "ns/removed", func(context.Context) ([]byte, error) {
			calls++
			if calls == 1 {
				close(started)
				<-gate
				return []byte("was there"), nil
			}
			return nil, nil
		})
		got <- string(data)
	}()
	<-started
	c.forget("ns/removed")
	close(gate)
	if v := <-got; v != "" {
		t.Errorf("after a removal during the read the asker got %q, want none", v)
	}

	// Someone who stops waiting is let go, and the read goes on without them.
	hold := make(chan struct{})
	impatient, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	began := time.Now()
	if data, ok := c.load(impatient, "ns/stalled", func(context.Context) ([]byte, error) { <-hold; return []byte("late"), nil }); ok || data != nil || time.Since(began) > time.Second {
		t.Errorf("an asker whose time ran out: %q %v after %v", data, ok, time.Since(began))
	}
	close(hold)
	for range 100 {
		if icon, fresh := c.get("ns/stalled"); fresh && string(icon.data) == "late" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("the read nobody waited for was not remembered")
}

// One remembered past its time is answered at once and read again behind
// the answer; a failure to read it again keeps what was known.
func TestServerIconsAnswerWhatTheyHaveWhileReadingAgain(t *testing.T) {
	ctx := t.Context()
	var c serverIcons
	if data, _ := c.load(ctx, "ns/world", func(context.Context) ([]byte, error) { return []byte("old"), nil }); string(data) != "old" {
		t.Fatalf("first read: %q", data)
	}
	// Older than it is fresh for.
	c.mu.Lock()
	c.icons["ns/world"] = serverIcon{data: []byte("old"), at: time.Now().Add(-2 * serverIconFor)}
	c.mu.Unlock()

	release := make(chan struct{})
	started := time.Now()
	data, ok := c.load(ctx, "ns/world", func(context.Context) ([]byte, error) {
		<-release
		return []byte("new"), nil
	})
	if !ok || string(data) != "old" || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("not answered at once with what it had: %q %v after %s", data, ok, time.Since(started))
	}
	close(release)
	for deadline := time.Now().Add(2 * time.Second); ; {
		if icon, fresh := c.get("ns/world"); fresh && string(icon.data) == "new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not read again behind the answer")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Past its time again, and its workspace cannot be reached: what was
	// known stays, rather than becoming no picture.
	c.mu.Lock()
	c.icons["ns/world"] = serverIcon{data: []byte("new"), at: time.Now().Add(-2 * serverIconFor)}
	c.mu.Unlock()
	if data, ok := c.load(ctx, "ns/world", func(context.Context) ([]byte, error) { return nil, errors.New("unreachable") }); !ok || string(data) != "new" {
		t.Fatalf("answered %q %v", data, ok)
	}
	for deadline := time.Now().Add(2 * time.Second); ; {
		c.mu.Lock()
		_, reading := c.reading["ns/world"]
		c.mu.Unlock()
		if !reading {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the read did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if icon, _ := c.get("ns/world"); string(icon.data) != "new" || icon.failed {
		t.Errorf("a failed read replaced what was known: %+v", icon)
	}
	// And it is tried again soon, not after the full time.
	c.mu.Lock()
	icon := c.icons["ns/world"]
	c.mu.Unlock()
	if !icon.retrySoon {
		t.Error("a failed read again is not tried again soon")
	}
	c.mu.Lock()
	c.icons["ns/world"] = serverIcon{data: icon.data, at: time.Now().Add(-serverIconRetry - time.Second), retrySoon: true}
	c.mu.Unlock()
	if _, fresh := c.get("ns/world"); fresh {
		t.Error("still fresh after the short wait")
	}
}

// Pictures answer only to who may see them, so the browser checks every time
// rather than keeping one for whoever signs in next.
func TestPicturesAreCheckedEveryTime(t *testing.T) {
	p := newPanel(t)
	if rec := p.send("operator", "PUT", "/me/avatar", asPNG(t, picture(64, 64))); rec.Code != 204 {
		t.Fatalf("setting an avatar: %d", rec.Code)
	}
	id := p.users["operator"].ID
	rec := p.send("viewer", "GET", "/users/"+id+"/avatar", nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "private, no-cache" || rec.Header().Get("ETag") == "" {
		t.Errorf("got %d, Cache-Control %q, ETag %q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
}

// A snapshot's picture is the server-icon.png among its files, redrawn as any
// picture the panel serves. One with none, or with a file there that is not
// a picture, has none.
func TestSnapshotIcon(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	dir := p.withOffsite("restic")
	repo, key := filepath.Join(dir, "alpha"), filepath.Join(dir, "alpha.key")
	restic := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("restic", append([]string{"--repo", repo, "--password-file", key, "-q"}, args...)...)
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("restic %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	if err := os.WriteFile(key, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restic(nil, "init")
	ids := func() map[string]bool {
		var list []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(restic(nil, "snapshots", "--json")), &list); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, s := range list {
			seen[s.ID[:8]] = true
		}
		return seen
	}
	take := func(name string, data []byte) string {
		before := ids()
		restic(data, "backup", "--stdin", "--stdin-filename", strings.TrimPrefix(name, "/"), "--host", "world", "--tag", "avsys", "--tag", "trigger=manual")
		for id := range ids() {
			if !before[id] {
				return id
			}
		}
		t.Fatal("no snapshot was taken")
		return ""
	}
	pictured := take(serverIconPath, asPNG(t, picture(128, 128)))
	none := take("/data/level.dat", []byte("a world"))
	notAPicture := take(serverIconPath, []byte("not a picture"))

	got := p.send("viewer", "GET", "/orgs/alpha/snapshots/"+pictured+"/icon", nil)
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("a snapshot with a picture: %d %s", got.Code, got.Body)
	}
	if img, err := png.Decode(got.Body); err != nil || img.Bounds().Dx() != serverIconSide {
		t.Errorf("not the panel's own %d pixel PNG: %v", serverIconSide, err)
	}
	for name, id := range map[string]string{"no picture": none, "not a picture": notAPicture, "no such snapshot": "deadbeef"} {
		if got := p.send("viewer", "GET", "/orgs/alpha/snapshots/"+id+"/icon", nil); got.Code != http.StatusNotFound {
			t.Errorf("%s: %d", name, got.Code)
		}
	}
	// A snapshot never changes, so what was read of it is kept.
	if _, ok := p.srv.snapshotIcons.get("alpha/" + pictured); !ok {
		t.Error("what was read of the snapshot was not kept")
	}
	// Until the organization's repository is gone with it.
	p.srv.snapshotIcons.set("beta/"+pictured, []byte("another's"))
	p.srv.snapshotIcons.forgetOrg("alpha")
	if _, ok := p.srv.snapshotIcons.get("alpha/" + pictured); ok {
		t.Error("what was read of a removed repository was kept")
	}
	if _, ok := p.srv.snapshotIcons.get("beta/" + pictured); !ok {
		t.Error("another organization's was dropped too")
	}
}

// A copy's picture is left out to keep the one its files bring, null for
// none, or base64 for its own, which is checked before anything is copied.
func TestPictureOf(t *testing.T) {
	if got, err := pictureOf(nil); err != nil || got.data != nil || got.remove {
		t.Errorf("left out: %+v %v", got, err)
	}
	if got, err := pictureOf(json.RawMessage(`null`)); err != nil || !got.remove {
		t.Errorf("null: %+v %v", got, err)
	}
	encoded, _ := json.Marshal(base64.StdEncoding.EncodeToString(asPNG(t, picture(300, 200))))
	got, err := pictureOf(encoded)
	if err != nil || got.remove {
		t.Fatalf("a picture: %+v %v", got, err)
	}
	if img, err := png.Decode(bytes.NewReader(got.data)); err != nil || img.Bounds().Dx() != serverIconSide || img.Bounds().Dy() != serverIconSide {
		t.Errorf("not redrawn to %d pixels: %v", serverIconSide, err)
	}
	for name, raw := range map[string]string{
		"not base64":    `"not base64!"`,
		"not a picture": `"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"`,
		"not a string":  `{"data":"x"}`,
	} {
		if _, err := pictureOf(json.RawMessage(raw)); err == nil {
			t.Errorf("%s was taken", name)
		}
	}

	// A server made from nothing is given its picture once it exists.
	p := newPanel(t)
	if got, code := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"pictured","picture":null,"spec":{"eula":true,"version":"1.21.4"}}`); got != 400 {
		t.Errorf("a picture with a new server: %d %s, want 400", got, code)
	}
}

// A load decided on before the icon was forgotten reads nothing, however
// late it starts.
func TestServerIconsLoadAsOf(t *testing.T) {
	ctx := t.Context()
	var c serverIcons
	c.init()
	asOf := c.changes["ns/gone"]
	c.forget("ns/gone")
	if data, ok := c.loadAsOf(ctx, "ns/gone", asOf, func(context.Context) ([]byte, error) {
		t.Error("read after it was forgotten")
		return []byte("old"), nil
	}); ok || data != nil {
		t.Errorf("got %q %v", data, ok)
	}
	if _, known := c.get("ns/gone"); known {
		t.Error("something was remembered for it")
	}
	// Unchanged, it reads as usual.
	if data, ok := c.loadAsOf(ctx, "ns/kept", c.changes["ns/kept"], func(context.Context) ([]byte, error) { return []byte("icon"), nil }); !ok || string(data) != "icon" {
		t.Errorf("unchanged: %q %v", data, ok)
	}
}
