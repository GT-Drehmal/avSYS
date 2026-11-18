package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GT-Drehmal/avSYS/internal/api"
)

// Servers whose name starts with "bingo" have files of their own here, as
// a server moved in from elsewhere has: mods put in their folder by hand,
// some of which Modrinth knows by their hash (see tests/upstream), and
// datapacks in its world. The panel finds them there as it would in a
// server's workspace, and can remove them. Every other server's files
// cannot be reached, as there is no cluster.
func looseFiles(dir string) func(ctx context.Context, namespace, name string) (api.ServerFiles, error) {
	var seeded sync.Map
	return func(_ context.Context, namespace, name string) (api.ServerFiles, error) {
		if !strings.HasPrefix(name, "bingo") {
			return nil, errors.New("nothing runs here: this panel has no cluster behind it")
		}
		root := filepath.Join(dir, "files", namespace, name)
		once, _ := seeded.LoadOrStore(root, &sync.Once{})
		var err error
		once.(*sync.Once).Do(func() { err = seedLoose(root) })
		if err != nil {
			return nil, err
		}
		return api.DirFiles(root), nil
	}
}

func seedLoose(root string) error {
	icon := func(r, g, b uint8) string {
		img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
		for y := range 16 {
			for x := range 16 {
				shade := uint8((x + y) * 4)
				img.Set(x, y, color.NRGBA{R: r + shade/4, G: g + shade/4, B: b, A: 255})
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		return buf.String()
	}
	files := map[string]string{
		// Modrinth has these, by their hash.
		"mods/bingo-2.9.6+mc1.20.1.jar":           "bingo 2.9.6 jar",
		"mods/fabric-api-0.92.2+1.20.1.jar":       "fabric api 0.92.2 jar",
		"mods/lithium-fabric-mc1.20.1-0.11.2.jar": "lithium 0.11.2 jar",
		// These say what they are.
		"mods/WI-Zoom-1.5-MC1.20.1.jar": zipOf(map[string]string{
			"fabric.mod.json":         `{"schemaVersion":1,"id":"wi_zoom","name":"WI Zoom","version":"1.5-MC1.20.1","icon":"assets/wi_zoom/icon.png"}`,
			"assets/wi_zoom/icon.png": icon(40, 120, 200),
		}),
		"mods/animatica-0.6+1.20.jar": zipOf(map[string]string{
			"fabric.mod.json": `{"schemaVersion":1,"id":"animatica","name":"Animatica","version":"0.6+1.20"}`,
		}),
		// And this, nothing: it is known by its name.
		"mods/c2me-fabric-mc1.20.1-0.2.0.jar": "not a jar that can be read",
		"world/datapacks/bern.zip": zipOf(map[string]string{
			"pack.mcmeta": `{"pack":{"pack_format":15,"description":"Bern's trades"}}`,
			"pack.png":    icon(200, 120, 40),
		}),
		"world/datapacks/hi_drehmal.zip": zipOf(map[string]string{"pack.mcmeta": `{"pack":{"pack_format":15,"description":{"text":"Hi, Drehmal"}}}`}),
		"world/level.dat":                "level",
		"server.properties":              "motd=Drehmal Bingo\n",
	}
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func zipOf(files map[string]string) string {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.String()
}
