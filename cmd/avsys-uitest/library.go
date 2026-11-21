package main

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/GT-Drehmal/avSYS/internal/api"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// seedLibrary gives an organization's library a world, as one uploaded,
// with datapacks in its world: two .zip files and two folders, two of them
// with a picture, and Bukkit's own, which is not one. The panel reads them
// from files here as it reads those of a world it unpacks. Its snapshot is
// in the repository's list (see seedSnapshots). It returns the world's ID.
func seedLibrary(ctx context.Context, srv *api.Server, dir string, org *store.Org) (string, error) {
	w := &store.World{OrgID: org.ID, Name: "Drehmal", Description: "The map of Drehmal: Primordial.", CreatedBy: "owner",
		Root: "world", Version: "1.20.1", File: "drehmal.zip", Size: 1_610_612_736, Unpacked: 1_610_612_736, Source: "upload"}
	if err := srv.Store.CreateWorld(ctx, w); err != nil {
		return "", err
	}
	packZip := func(meta string, icon []byte) ([]byte, error) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		files := map[string][]byte{"pack.mcmeta": []byte(meta), "data/drehmal/functions/tick.mcfunction": []byte("say hi\n")}
		if icon != nil {
			files["pack.png"] = icon
		}
		for name, body := range files {
			f, err := zw.Create(name)
			if err != nil {
				return nil, err
			}
			if _, err := f.Write(body); err != nil {
				return nil, err
			}
		}
		err := zw.Close()
		return buf.Bytes(), err
	}
	core, err := packZip(`{"pack":{"pack_format":15,"description":"Quests, bosses and lore of Drehmal"}}`, packIcon(120, 60, 200))
	if err != nil {
		return "", err
	}
	trades, err := packZip(`{"pack":{"pack_format":15,"description":"Villager trades of Bern"}}`, nil)
	if err != nil {
		return "", err
	}
	root := filepath.Join(dir, "library", w.ID)
	files := map[string][]byte{
		"world/level.dat":                        []byte("Drehmal"),
		"world/datapacks/drehmal_core.zip":       core,
		"world/datapacks/bern_trades.zip":        trades,
		"world/datapacks/mael/pack.mcmeta":       []byte(`{"pack":{"pack_format":15,"description":"Mael's music discs"}}`),
		"world/datapacks/mael/pack.png":          packIcon(40, 140, 220),
		"world/datapacks/hi_drehmal/pack.mcmeta": []byte(`{"pack":{"pack_format":15,"description":[{"text":"Hi, "},{"text":"§bDrehmal"}]}}`),
		"world/datapacks/bukkit/pack.mcmeta":     []byte(`{"pack":{"pack_format":15,"description":"Data pack for resources provided by Bukkit plugins"}}`),
	}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(root, name), body, 0o644); err != nil {
			return "", err
		}
	}
	return w.ID, srv.NoteWorldDatapacks(ctx, org.ID, w.ID, api.DirFiles(root), "world")
}

// packIcon is a datapack's picture: a square of one colour, shaded.
func packIcon(r, g, b uint8) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			shade := uint8((x + y) * 2)
			img.Set(x, y, color.NRGBA{R: r + shade/2, G: g + shade/2, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
