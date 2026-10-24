package packmerge

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
)

// iconSize is the side of the icon made for packs joined: sharp at the
// largest size the game draws a pack's icon.
const iconSize = 128

// iconLimit is the most pixels on a side of an icon read. A bigger one is
// passed over, not decoded.
const iconLimit = 2048

// joinIcons makes one icon of the packs' own, the top one first, read left
// to right and down: two side by side, three as two over one, and the top
// four in a two by two grid. An icon in a part not square shows its middle.
// With fewer than two to be read it makes none.
func joinIcons(icons []*zip.File) []byte {
	var imgs []image.Image
	for _, f := range icons {
		if img := readIcon(f); img != nil {
			imgs = append(imgs, img)
		}
		if len(imgs) == 4 {
			break
		}
	}
	const s, h = iconSize, iconSize / 2
	var parts []image.Rectangle
	switch len(imgs) {
	case 0, 1:
		return nil
	case 2:
		parts = []image.Rectangle{image.Rect(0, 0, h, s), image.Rect(h, 0, s, s)}
	case 3:
		parts = []image.Rectangle{image.Rect(0, 0, h, h), image.Rect(h, 0, s, h), image.Rect(0, h, s, s)}
	default:
		parts = []image.Rectangle{image.Rect(0, 0, h, h), image.Rect(h, 0, s, h), image.Rect(0, h, h, s), image.Rect(h, h, s, s)}
	}
	out := image.NewNRGBA(image.Rect(0, 0, s, s))
	for i, r := range parts {
		scaleInto(out, r, imgs[i], middle(imgs[i].Bounds(), r.Dx(), r.Dy()))
	}
	var b bytes.Buffer
	if err := png.Encode(&b, out); err != nil {
		return nil
	}
	return b.Bytes()
}

// middle is the most of b, about its middle, that is as wide for its
// height as w is for h.
func middle(b image.Rectangle, w, h int) image.Rectangle {
	if b.Dx()*h > b.Dy()*w {
		cw := max(b.Dy()*w/h, 1)
		x := b.Min.X + (b.Dx()-cw)/2
		return image.Rect(x, b.Min.Y, x+cw, b.Max.Y)
	}
	ch := max(b.Dx()*h/w, 1)
	y := b.Min.Y + (b.Dy()-ch)/2
	return image.Rect(b.Min.X, y, b.Max.X, y+ch)
}

// readIcon is a pack's icon, or nil for one that is not a PNG or is too big.
func readIcon(f *zip.File) image.Image {
	r, err := f.Open()
	if err != nil {
		return nil
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, jsonLimit+1))
	if err != nil || len(data) > jsonLimit {
		return nil
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > iconLimit || cfg.Height > iconLimit {
		return nil
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return img
}

// scaleInto draws the part b of img over the whole of r: each pixel made is
// the average of those of img it covers, so pixel art made bigger keeps its
// edges, and a big icon made smaller is not grainy.
func scaleInto(dst *image.NRGBA, r image.Rectangle, img image.Image, b image.Rectangle) {
	w, h := r.Dx(), r.Dy()
	for y := 0; y < h; y++ {
		y0 := b.Min.Y + y*b.Dy()/h
		y1 := max(b.Min.Y+(y+1)*b.Dy()/h, y0+1)
		for x := 0; x < w; x++ {
			x0 := b.Min.X + x*b.Dx()/w
			x1 := max(b.Min.X+(x+1)*b.Dx()/w, x0+1)
			// Averaged weighted by how opaque each is, so a clear pixel's
			// colour does not tint those beside it; of a big icon, at most
			// eight by eight of them.
			dy, dx := max((y1-y0)/8, 1), max((x1-x0)/8, 1)
			var sr, sg, sb, sa, n uint64
			for sy := y0; sy < y1; sy += dy {
				for sx := x0; sx < x1; sx += dx {
					c := color.NRGBA64Model.Convert(img.At(sx, sy)).(color.NRGBA64)
					sr += uint64(c.R) * uint64(c.A)
					sg += uint64(c.G) * uint64(c.A)
					sb += uint64(c.B) * uint64(c.A)
					sa += uint64(c.A)
					n++
				}
			}
			var c color.NRGBA
			if sa > 0 {
				c = color.NRGBA{R: uint8(sr / sa >> 8), G: uint8(sg / sa >> 8), B: uint8(sb / sa >> 8), A: uint8(sa / n >> 8)}
			}
			dst.SetNRGBA(r.Min.X+x, r.Min.Y+y, c)
		}
	}
}
