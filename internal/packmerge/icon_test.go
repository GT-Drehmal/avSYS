package packmerge

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// icon is a PNG of one colour, this many pixels on a side, with its top
// left pixel clear.
func icon(t *testing.T, side int, c color.NRGBA) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	img.SetNRGBA(0, 0, color.NRGBA{})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

var (
	red   = color.NRGBA{R: 255, A: 255}
	green = color.NRGBA{G: 255, A: 255}
	blue  = color.NRGBA{B: 255, A: 255}
	white = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	gold  = color.NRGBA{R: 255, G: 200, A: 255}
)

// at is the colour of a joined icon at each of these points.
func at(t *testing.T, data string, points ...image.Point) []color.NRGBA {
	t.Helper()
	img, err := png.Decode(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != iconSize || b.Dy() != iconSize {
		t.Fatalf("icon is %v", b)
	}
	var out []color.NRGBA
	for _, p := range points {
		out = append(out, color.NRGBAModel.Convert(img.At(p.X, p.Y)).(color.NRGBA))
	}
	return out
}

// Points in each part of an icon of two, three and four.
var (
	halves     = []image.Point{{32, 10}, {32, 118}, {96, 10}, {96, 118}}
	twoOverOne = []image.Point{{32, 32}, {96, 32}, {10, 96}, {118, 96}}
	quarters   = []image.Point{{32, 32}, {96, 32}, {32, 96}, {96, 96}}
)

func TestIconsAreShownTogether(t *testing.T) {
	packOf := func(name string, c color.NRGBA, side int) Pack {
		return pack(t, name, map[string]string{"pack.mcmeta": meta, "pack.png": icon(t, side, c)})
	}
	for _, tc := range []struct {
		name   string
		packs  []Pack
		points []image.Point
		want   []color.NRGBA
	}{
		{"two, side by side", []Pack{packOf("A", red, 16), packOf("B", blue, 512)}, halves, []color.NRGBA{red, red, blue, blue}},
		{"three, two over one", []Pack{packOf("A", red, 64), packOf("B", blue, 64), packOf("C", green, 64)}, twoOverOne, []color.NRGBA{red, blue, green, green}},
		{"the top four of five", []Pack{packOf("A", red, 32), packOf("B", blue, 32), packOf("C", green, 32), packOf("D", white, 32), packOf("E", gold, 32)}, quarters, []color.NRGBA{red, blue, green, white}},
		{"one with none is passed over", []Pack{packOf("A", red, 32), pack(t, "B", map[string]string{"pack.mcmeta": meta}), packOf("C", green, 32)}, halves, []color.NRGBA{red, red, green, green}},
		{"one not a picture is passed over", []Pack{packOf("A", red, 32), pack(t, "B", map[string]string{"pack.mcmeta": meta, "pack.png": "not a picture"}), packOf("C", green, 32)}, halves, []color.NRGBA{red, red, green, green}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, _ := merge(t, 0, tc.packs...)
			got := at(t, files["pack.png"], tc.points...)
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("at %v is %v, want %v", tc.points[i], got[i], tc.want[i])
				}
			}
		})
	}
}

func TestAHalfShowsTheMiddleOfItsIcon(t *testing.T) {
	// Red on the left and right quarters, blue in the middle half: in a
	// half, only the blue is shown, not squeezed.
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := blue
			if x < 16 || x >= 48 {
				c = red
			}
			img.SetNRGBA(x, y, c)
		}
	}
	if got := middle(img.Bounds(), 64, 128); got != image.Rect(16, 0, 48, 64) {
		t.Errorf("middle of a tall part is %v", got)
	}
	if got := middle(img.Bounds(), 128, 64); got != image.Rect(0, 16, 64, 48) {
		t.Errorf("middle of a wide part is %v", got)
	}
	out := image.NewNRGBA(image.Rect(0, 0, 64, 128))
	scaleInto(out, out.Bounds(), img, middle(img.Bounds(), 64, 128))
	for _, x := range []int{0, 63} {
		if c := out.NRGBAAt(x, 64); c != blue {
			t.Errorf("at %d is %v", x, c)
		}
	}
}

func TestOneIconIsKeptAsItIs(t *testing.T) {
	own := icon(t, 16, red)
	files, _ := merge(t, 0, pack(t, "A", map[string]string{"pack.mcmeta": meta, "pack.png": own}), pack(t, "B", map[string]string{"pack.mcmeta": meta}))
	if files["pack.png"] != own {
		t.Error("the only icon was changed")
	}
	// Only one a picture: the top one's, as it is.
	files, _ = merge(t, 0, pack(t, "A", map[string]string{"pack.mcmeta": meta, "pack.png": "not a picture"}), pack(t, "B", map[string]string{"pack.mcmeta": meta, "pack.png": own}))
	if files["pack.png"] != "not a picture" {
		t.Errorf("icon is %q", files["pack.png"])
	}
}

func TestIconsClearPixelsDoNotTint(t *testing.T) {
	// Made smaller, a clear pixel among red ones leaves them red.
	img, err := png.Decode(strings.NewReader(icon(t, 512, red)))
	if err != nil {
		t.Fatal(err)
	}
	out := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	scaleInto(out, out.Bounds(), img, img.Bounds())
	if c := out.NRGBAAt(0, 0); c.R != 255 || c.G != 0 || c.B != 0 || c.A == 0 {
		t.Errorf("corner is %v", c)
	}
}

func TestTooBigAnIconIsPassedOver(t *testing.T) {
	big := image.NewNRGBA(image.Rect(0, 0, iconLimit+1, 1))
	var b bytes.Buffer
	if err := png.Encode(&b, big); err != nil {
		t.Fatal(err)
	}
	files, _ := merge(t, 0, pack(t, "A", map[string]string{"pack.mcmeta": meta, "pack.png": b.String()}), pack(t, "B", map[string]string{"pack.mcmeta": meta, "pack.png": icon(t, 16, red)}))
	if files["pack.png"] != b.String() {
		t.Error("with one to read, the top one's should be kept as it is")
	}
}
