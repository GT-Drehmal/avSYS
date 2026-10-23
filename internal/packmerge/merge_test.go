package packmerge

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// pack makes a pack of these files, by name.
func pack(t *testing.T, name string, files map[string]string) Pack {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return Pack{Name: name, Zip: r}
}

const meta = `{"pack":{"pack_format":34,"description":"x"}}`

// merge joins the packs, the top one first, and returns the files of the
// pack made, with what was found.
func merge(t *testing.T, format int, packs ...Pack) (map[string]string, *Result) {
	t.Helper()
	var buf bytes.Buffer
	res, err := Merge(&buf, packs, format)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range r.File {
		data, err := readFile(f)
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(data)
	}
	return files, res
}

// same says whether two JSON texts say the same.
func same(t *testing.T, got, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(got), &a); err != nil {
		t.Fatalf("%v in %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("%v in the test's %s", err, want)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Where two packs have the same file, the top one's is used, and the files
// of the one under that differ are said to be hidden.
func TestTheTopPackWins(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "pack.png": "top icon", "assets/minecraft/textures/block/stone.png": "top stone", "assets/minecraft/textures/block/dirt.png": "same dirt"})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, "pack.png": "under icon", "assets/minecraft/textures/block/stone.png": "under stone", "assets/minecraft/textures/block/dirt.png": "same dirt", "assets/minecraft/textures/block/sand.png": "sand"})
	files, res := merge(t, 0, top, under)
	for name, want := range map[string]string{"pack.png": "top icon", "assets/minecraft/textures/block/stone.png": "top stone", "assets/minecraft/textures/block/dirt.png": "same dirt", "assets/minecraft/textures/block/sand.png": "sand"} {
		if files[name] != want {
			t.Errorf("%s is %q, want %q", name, files[name], want)
		}
	}
	// The icon is not one pack's hiding another's; the same dirt is not either.
	want := []Hidden{{Over: 0, Under: 1, Files: []string{"assets/minecraft/textures/block/stone.png"}}}
	if !reflect.DeepEqual(res.Hidden, want) {
		t.Errorf("hidden %+v, want %+v", res.Hidden, want)
	}
}

// The pack made says the format of the game it is for, the old way or the
// new as that game reads it, and keeps every pack's overlays, from the
// bottom pack's up.
func TestPackMeta(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": `{"pack":{"pack_format":34,"description":"x"},"overlays":{"entries":[{"formats":[30,40],"directory":"top_new"}]},"language":{"xx_yy":{"name":"Top"}}}`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": `{"pack":{"pack_format":15,"description":"y"},"overlays":{"entries":[{"formats":[18,40],"directory":"under_new"}]},"filter":{"block":[{"namespace":"minecraft","path":"textures/x"}]},"language":{"xx_yy":{"name":"Under"},"zz_zz":{"name":"Z"}}}`})
	// With the format known, overlays are laid over the files already.
	files, _ := merge(t, 46, top, under)
	same(t, files["pack.mcmeta"], `{
		"pack": {"description": "Top, Under", "pack_format": 46},
		"filter": {"block": [{"namespace":"minecraft","path":"textures/x"}]},
		"language": {"xx_yy": {"name":"Top"}, "zz_zz": {"name":"Z"}}
	}`)
	files, _ = merge(t, 69, top, under)
	same(t, files["pack.mcmeta"], `{
		"pack": {"description": "Top, Under", "min_format": 69, "max_format": 69},
		"filter": {"block": [{"namespace":"minecraft","path":"textures/x"}]},
		"language": {"xx_yy": {"name":"Top"}, "zz_zz": {"name":"Z"}}
	}`)
	// Not knowing the game's, what the top pack says.
	files, _ = merge(t, 0, top, under)
	same(t, files["pack.mcmeta"], `{
		"pack": {"description": "Top, Under", "pack_format": 34},
		"overlays": {"entries": [{"formats":[18,40],"directory":"under_new"}, {"formats":[30,40],"directory":"top_new"}]},
		"filter": {"block": [{"namespace":"minecraft","path":"textures/x"}]},
		"language": {"xx_yy": {"name":"Top"}, "zz_zz": {"name":"Z"}}
	}`)
}

func TestLangFilesAreJoined(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "assets/minecraft/lang/en_us.json": `{"block.minecraft.stone":"Top Stone","item.top":"Top"}`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, "assets/minecraft/lang/en_us.json": `{"block.minecraft.stone":"Under Stone","item.under":"Under"}`})
	files, res := merge(t, 0, top, under)
	same(t, files["assets/minecraft/lang/en_us.json"], `{"block.minecraft.stone":"Top Stone","item.top":"Top","item.under":"Under"}`)
	if len(res.Hidden) != 0 {
		t.Errorf("joined files said to be hidden: %+v", res.Hidden)
	}
}

// As the game joins them: an event that replaces is the top one's alone;
// otherwise the sounds of both are played.
func TestSoundsAreJoined(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "assets/minecraft/sounds.json": `{
		"block.stone.break": {"sounds": ["top/break"], "subtitle": "top.subtitle"},
		"music.menu": {"replace": true, "sounds": ["top/menu"]},
		"top.only": {"sounds": ["top/only"], "subtitle": "top.subtitle"}
	}`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, "assets/minecraft/sounds.json": `{
		"block.stone.break": {"replace": true, "sounds": ["under/break"], "subtitle": "under.subtitle"},
		"music.menu": {"sounds": ["under/menu"]},
		"under.only": {"sounds": ["under/only"]}
	}`})
	files, _ := merge(t, 0, top, under)
	same(t, files["assets/minecraft/sounds.json"], `{
		"block.stone.break": {"replace": true, "sounds": ["under/break", "top/break"], "subtitle": "under.subtitle"},
		"music.menu": {"replace": true, "sounds": ["top/menu"]},
		"top.only": {"sounds": ["top/only"], "subtitle": "top.subtitle"},
		"under.only": {"sounds": ["under/only"]}
	}`)
}

// Fonts and atlases are lists the game joins from every pack. The first
// font provider with a character draws it, so the top pack's come first;
// atlases' sources go the bottom pack's first, as the game joins them.
func TestFontsAndAtlasesAreJoined(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta,
		"assets/minecraft/font/default.json":   `{"providers":[{"type":"bitmap","file":"top.png","chars":[""]}]}`,
		"assets/minecraft/atlases/blocks.json": `{"sources":[{"type":"directory","source":"top","prefix":"top/"}]}`,
	})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta,
		"assets/minecraft/font/default.json":   `{"providers":[{"type":"bitmap","file":"under.png","chars":[""]}]}`,
		"assets/minecraft/atlases/blocks.json": `{"sources":[{"type":"directory","source":"under","prefix":"under/"}]}`,
	})
	files, _ := merge(t, 0, top, under)
	same(t, files["assets/minecraft/font/default.json"], `{"providers":[{"type":"bitmap","file":"top.png","chars":[""]},{"type":"bitmap","file":"under.png","chars":[""]}]}`)
	same(t, files["assets/minecraft/atlases/blocks.json"], `{"sources":[{"type":"directory","source":"under","prefix":"under/"},{"type":"directory","source":"top","prefix":"top/"}]}`)
}

// Before 1.21.4: the item model is the top one's, with the overrides of
// both, so the custom items of each keep working.
func TestItemModelOverridesAreJoined(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "assets/minecraft/models/item/diamond_sword.json": `{"parent":"item/handheld","textures":{"layer0":"top/sword"},"overrides":[{"predicate":{"custom_model_data":2},"model":"top/blade"}]}`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, "assets/minecraft/models/item/diamond_sword.json": `{"parent":"item/handheld","textures":{"layer0":"under/sword"},"overrides":[{"predicate":{"custom_model_data":1},"model":"under/blade"}]}`})
	files, res := merge(t, 0, top, under)
	same(t, files["assets/minecraft/models/item/diamond_sword.json"], `{"parent":"item/handheld","textures":{"layer0":"top/sword"},"overrides":[{"predicate":{"custom_model_data":1},"model":"under/blade"},{"predicate":{"custom_model_data":2},"model":"top/blade"}]}`)
	if len(res.Hidden) != 0 {
		t.Errorf("joined files said to be hidden: %+v", res.Hidden)
	}
	// One under with overrides keeps them under a top one with none.
	top = pack(t, "Top", map[string]string{"pack.mcmeta": meta, "assets/minecraft/models/item/diamond_sword.json": `{"parent":"item/handheld","textures":{"layer0":"top/sword"}}`})
	files, _ = merge(t, 0, top, under)
	same(t, files["assets/minecraft/models/item/diamond_sword.json"], `{"parent":"item/handheld","textures":{"layer0":"top/sword"},"overrides":[{"predicate":{"custom_model_data":1},"model":"under/blade"}]}`)
}

// From 1.21.4: item definitions that pick their model by the same property
// are joined, the top one's entries winning where both have one for the
// same value.
func TestItemDefinitionsAreJoined(t *testing.T) {
	const name = "assets/minecraft/items/diamond_sword.json"
	dispatch := func(entries, fallback string) string {
		return `{"model":{"type":"minecraft:range_dispatch","property":"minecraft:custom_model_data","entries":` + entries + `,"fallback":` + fallback + `}}`
	}
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, name: dispatch(`[{"threshold":1,"model":{"type":"model","model":"top/one"}},{"threshold":2,"model":{"type":"model","model":"top/two"}}]`, `{"type":"model","model":"top/sword"}`)})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, name: dispatch(`[{"threshold":1,"model":{"type":"model","model":"under/one"}},{"threshold":3,"model":{"type":"model","model":"under/three"}}]`, `{"type":"model","model":"under/sword"}`)})
	files, res := merge(t, 0, top, under)
	same(t, files[name], dispatch(`[{"threshold":1,"model":{"type":"model","model":"top/one"}},{"threshold":2,"model":{"type":"model","model":"top/two"}},{"threshold":3,"model":{"type":"model","model":"under/three"}}]`, `{"type":"model","model":"top/sword"}`))
	if len(res.Hidden) != 0 {
		t.Errorf("joined files said to be hidden: %+v", res.Hidden)
	}

	// A plain model over one that picks is what it falls back to.
	plain := pack(t, "Plain", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"minecraft:model","model":"plain/sword"}}`})
	files, _ = merge(t, 0, plain, under)
	same(t, files[name], dispatch(`[{"threshold":1,"model":{"type":"model","model":"under/one"}},{"threshold":3,"model":{"type":"model","model":"under/three"}}]`, `{"type":"minecraft:model","model":"plain/sword"}`))

	// One that picks over a plain one with no fallback falls back to it.
	files, _ = merge(t, 0, pack(t, "Top", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"range_dispatch","property":"custom_model_data","entries":[]}}`}), plain)
	same(t, files[name], `{"model":{"type":"range_dispatch","property":"custom_model_data","entries":[],"fallback":{"type":"minecraft:model","model":"plain/sword"}}}`)

	// Cases of a select: the top one's first, as the first that fits is used.
	sel := func(cases string) string {
		return `{"model":{"type":"select","property":"minecraft:component","component":"minecraft:custom_name","cases":` + cases + `}}`
	}
	top = pack(t, "Top", map[string]string{"pack.mcmeta": meta, name: sel(`[{"when":"Excalibur","model":{"type":"model","model":"top/excalibur"}}]`)})
	under = pack(t, "Under", map[string]string{"pack.mcmeta": meta, name: sel(`[{"when":"Excalibur","model":{"type":"model","model":"under/excalibur"}},{"when":"Sting","model":{"type":"model","model":"under/sting"}}]`)})
	files, _ = merge(t, 0, top, under)
	same(t, files[name], sel(`[{"when":"Excalibur","model":{"type":"model","model":"top/excalibur"}},{"when":"Sting","model":{"type":"model","model":"under/sting"}}]`))

	// A case for several values, some of which the top one has a case for,
	// is kept for the rest: the game takes no two cases for one value.
	under = pack(t, "Under", map[string]string{"pack.mcmeta": meta, name: sel(`[{"when":["Excalibur","Sting","Glamdring"],"model":{"type":"model","model":"under/blade"}},{"when":["Excalibur"],"model":{"type":"model","model":"under/excalibur"}}]`)})
	files, _ = merge(t, 0, top, under)
	same(t, files[name], sel(`[{"when":"Excalibur","model":{"type":"model","model":"top/excalibur"}},{"when":["Sting","Glamdring"],"model":{"type":"model","model":"under/blade"}}]`))
}

// Two that pick by custom model data in different ways, here by different
// numbers of it, cannot be joined: the top one's is used, and the other is
// said to be hidden.
func TestItemDefinitionsThatCannotBeJoined(t *testing.T) {
	const name = "assets/minecraft/items/diamond_sword.json"
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"range_dispatch","property":"minecraft:custom_model_data","index":1,"entries":[]}}`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"range_dispatch","property":"minecraft:custom_model_data","entries":[]}}`})
	files, res := merge(t, 0, top, under)
	same(t, files[name], `{"model":{"type":"range_dispatch","property":"minecraft:custom_model_data","index":1,"entries":[]}}`)
	want := []Hidden{{Over: 0, Under: 1, Files: []string{name}}}
	if !reflect.DeepEqual(res.Hidden, want) {
		t.Errorf("hidden %+v, want %+v", res.Hidden, want)
	}

	// One under that picks by where it is held is how that pack draws it,
	// not which custom item it is: the plain one over is used.
	top = pack(t, "Top", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"model","model":"top/sword"}}`})
	under = pack(t, "Under", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"select","property":"display_context","cases":[{"when":"gui","model":{"type":"model","model":"under/flat"}}]}}`})
	files, res = merge(t, 0, top, under)
	same(t, files[name], `{"model":{"type":"model","model":"top/sword"}}`)
	if len(res.Hidden) != 1 {
		t.Errorf("hidden %+v, want the one under's", res.Hidden)
	}
}

// A file the game could not read is not joined: the top one's is used.
func TestBrokenJSONIsNotJoined(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "assets/minecraft/lang/en_us.json": `{"a":`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, "assets/minecraft/lang/en_us.json": `{"b":"B"}`})
	files, res := merge(t, 0, top, under)
	if files["assets/minecraft/lang/en_us.json"] != `{"a":` || len(res.Hidden) != 1 {
		t.Errorf("made %q, hidden %+v", files["assets/minecraft/lang/en_us.json"], res.Hidden)
	}
}

// The files of overlays are joined as those at the top of a pack are.
func TestOverlayFilesAreJoined(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "new/assets/minecraft/lang/en_us.json": `{"a":"A"}`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, "new/assets/minecraft/lang/en_us.json": `{"b":"B"}`})
	files, _ := merge(t, 0, top, under)
	same(t, files["new/assets/minecraft/lang/en_us.json"], `{"a":"A","b":"B"}`)
}

// The same packs in the same order make the same zip, byte for byte; in
// another order, another.
func TestTheSameZipEachTime(t *testing.T) {
	build := func(first, second Pack) []byte {
		var buf bytes.Buffer
		if _, err := Merge(&buf, []Pack{first, second}, 34); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	a := pack(t, "A", map[string]string{"pack.mcmeta": meta, "x.png": "a", "assets/minecraft/lang/en_us.json": `{"a":"A"}`})
	b := pack(t, "B", map[string]string{"pack.mcmeta": meta, "x.png": "b", "assets/minecraft/lang/en_us.json": `{"b":"B"}`})
	if !bytes.Equal(build(a, b), build(a, b)) {
		t.Error("the same packs made different zips")
	}
	if bytes.Equal(build(a, b), build(b, a)) {
		t.Error("packs in another order made the same zip")
	}
}

// Names a game would not read, or that would leave the pack's folder, are
// left out.
func TestNamesOutsideThePack(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "../evil.png": "x", "/abs.png": "x", "./assets/minecraft/a.png": "a"})
	files, _ := merge(t, 0, top)
	if _, ok := files["../evil.png"]; ok {
		t.Error("kept a name leading out of the pack")
	}
	if _, ok := files["/abs.png"]; ok {
		t.Error("kept a name from the root")
	}
	if files["assets/minecraft/a.png"] != "a" {
		t.Errorf("did not keep ./assets/minecraft/a.png as assets/minecraft/a.png: %v", files)
	}
}

// A lower pack's overlay is laid over its own files, not over the top
// pack's: in one pack it would cover what the top one has. Overlays not for
// the format are left out.
func TestOverlaysAreLaidOverTheirOwnPack(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, "assets/minecraft/textures/block/stone.png": "top stone"})
	under := pack(t, "Under", map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":34,"description":"y"},"overlays":{"entries":[{"formats":[40,50],"directory":"new"},{"formats":{"min_inclusive":10,"max_inclusive":20},"directory":"old"},{"min_format":46,"max_format":[50,0],"directory":"newer"}]}}`,
		"assets/minecraft/textures/block/dirt.png":       "under dirt",
		"new/assets/minecraft/textures/block/stone.png":  "under new stone",
		"new/assets/minecraft/textures/block/dirt.png":   "under new dirt",
		"newer/assets/minecraft/textures/block/dirt.png": "under newer dirt",
		"old/assets/minecraft/textures/block/sand.png":   "under old sand",
	})
	files, _ := merge(t, 46, top, under)
	for name, want := range map[string]string{
		"assets/minecraft/textures/block/stone.png": "top stone",
		// The later overlay over the earlier.
		"assets/minecraft/textures/block/dirt.png": "under newer dirt",
	} {
		if files[name] != want {
			t.Errorf("%s is %q, want %q", name, files[name], want)
		}
	}
	for name := range files {
		if strings.HasPrefix(name, "new/") || strings.HasPrefix(name, "old/") || strings.HasPrefix(name, "newer/") || strings.HasSuffix(name, "sand.png") {
			t.Errorf("kept %s", name)
		}
	}
}

// A pack's block filters hide what packs under it have of those assets,
// not its own; the pack made keeps them, for any pack under it.
func TestFiltersHideWhatIsUnder(t *testing.T) {
	top := pack(t, "Top", map[string]string{
		"pack.mcmeta": `{"pack":{"pack_format":34,"description":"x"},"filter":{"block":[{"namespace":"minecraft","path":"textures/block/sa.*"}]}}`,
		"assets/minecraft/textures/block/sandstone.png": "top sandstone",
	})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta,
		"assets/minecraft/textures/block/sand.png":      "under sand",
		"assets/minecraft/textures/block/sandstone.png": "under sandstone",
		"assets/minecraft/textures/block/stone.png":     "under stone",
	})
	files, res := merge(t, 34, top, under)
	if _, ok := files["assets/minecraft/textures/block/sand.png"]; ok {
		t.Error("kept what the top pack filters out of the one under")
	}
	if files["assets/minecraft/textures/block/sandstone.png"] != "top sandstone" || files["assets/minecraft/textures/block/stone.png"] != "under stone" {
		t.Errorf("files %v", files)
	}
	// Filtered out, it is not hidden by the top one: it is not there.
	if len(res.Hidden) != 0 {
		t.Errorf("hidden %+v", res.Hidden)
	}
	same(t, files["pack.mcmeta"], `{"pack":{"description":"Top, Under","pack_format":34},"filter":{"block":[{"namespace":"minecraft","path":"textures/block/sa.*"}]}}`)
}

// What a texture is drawn with, in the .mcmeta beside it, is of the pack it
// is from, or one over it; one under's is not used for another's texture.
func TestTextureMetaGoesWithItsTexture(t *testing.T) {
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta,
		"assets/minecraft/textures/block/stone.png":       "top stone",
		"assets/minecraft/textures/block/sand.png.mcmeta": `{"animation":{"frametime":1}}`,
	})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta,
		"assets/minecraft/textures/block/stone.png":        "under stone",
		"assets/minecraft/textures/block/stone.png.mcmeta": `{"animation":{}}`,
		"assets/minecraft/textures/block/dirt.png":         "under dirt",
		"assets/minecraft/textures/block/dirt.png.mcmeta":  `{"animation":{}}`,
		"assets/minecraft/textures/block/sand.png":         "under sand",
	})
	files, _ := merge(t, 0, top, under)
	if _, ok := files["assets/minecraft/textures/block/stone.png.mcmeta"]; ok {
		t.Error("the top pack's stone is drawn with the .mcmeta of the one under")
	}
	if files["assets/minecraft/textures/block/dirt.png.mcmeta"] == "" {
		t.Error("lost the .mcmeta of a texture only the pack under has")
	}
	if files["assets/minecraft/textures/block/sand.png.mcmeta"] != `{"animation":{"frametime":1}}` {
		t.Error("lost the .mcmeta a pack over the texture's gives it")
	}
}

// The same type and property, written with their namespace or without, are
// the same: the items of both are joined.
func TestItemDefinitionsWithAndWithoutNamespace(t *testing.T) {
	const name = "assets/minecraft/items/stick.json"
	top := pack(t, "Top", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"minecraft:range_dispatch","property":"minecraft:custom_model_data","entries":[{"threshold":1,"model":{"type":"model","model":"top/one"}}]}}`})
	under := pack(t, "Under", map[string]string{"pack.mcmeta": meta, name: `{"model":{"type":"range_dispatch","property":"custom_model_data","entries":[{"threshold":2,"model":{"type":"model","model":"under/two"}}]}}`})
	files, res := merge(t, 0, top, under)
	same(t, files[name], `{"model":{"type":"minecraft:range_dispatch","property":"minecraft:custom_model_data","entries":[{"threshold":1,"model":{"type":"model","model":"top/one"}},{"threshold":2,"model":{"type":"model","model":"under/two"}}]}}`)
	if len(res.Hidden) != 0 {
		t.Errorf("hidden %+v", res.Hidden)
	}
}
