// Package packmerge joins several resource packs into one, as a server can
// offer players only one.
//
// The packs are in order, the top one first, as in the game's own list of
// packs: where two have the same file, the one higher up is used. Some files
// the game reads from every pack and joins itself, and those are joined here
// the same way: sounds.json, the lang files, fonts and atlases. Item models
// are joined too, which the game does not do, so that the custom items of
// every pack keep working.
//
// The same packs in the same order always make the same zip, byte for byte,
// so a pack built again from what has not changed has the same hash.
package packmerge

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Pack is one pack to join.
type Pack struct {
	// Name is how the pack is spoken of in the pack.mcmeta made.
	Name string
	Zip  *zip.Reader
}

// Hidden is the files of one pack that one higher up has too, and which are
// used from that one instead.
type Hidden struct {
	// Over and Under are the packs, by where they are in the list.
	Over, Under int
	Files       []string
}

// Result is what joining the packs found.
type Result struct {
	// Hidden is, for each pair of packs that have files in common that
	// could not be joined, which files those are. Files that are the same
	// in both are not counted.
	Hidden []Hidden
}

// jsonLimit is the most of one JSON file that is read to be joined.
const jsonLimit = 16 << 20

// stamp is the time every file in the zip made is said to be from, so that
// the zip is the same however often it is made.
var stamp = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// Merge writes the packs, the top one first, joined into one zip. format is
// the pack format the game it is for reads, which the pack made says it is
// for, and which decides which of each pack's overlays are used: they are
// laid over its own files here, as the game would, since in one pack an
// overlay of one under would cover what one over it has. With 0, it says
// what the top pack says, and every overlay is kept as it is.
func Merge(w io.Writer, packs []Pack, format int) (*Result, error) {
	if len(packs) == 0 {
		return nil, errors.New("no packs to join")
	}
	metas := make([]packMeta, len(packs))
	for i, p := range packs {
		var err error
		if metas[i], err = readMeta(p); err != nil {
			return nil, err
		}
	}
	// Each pack's files, as the game reads them.
	own := make([]map[string]*zip.File, len(packs))
	for i := range packs {
		own[i] = filesOf(packs[i], metas[i], format)
	}
	// A pack's block filters hide what packs under it have, not its own:
	// what they hide of those is left out here. They are kept in the pack
	// made, for any pack under it.
	for i := range packs {
		for j := 0; j < i; j++ {
			hideFiltered(own[i], metas[j].Filter.Block)
		}
	}
	// Each file, and the packs that have it, the top one first.
	files := map[string][]entry{}
	for i := range packs {
		for name, f := range own[i] {
			files[name] = append(files[name], entry{pack: i, file: f})
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	out := zip.NewWriter(w)
	meta, err := mergeMeta(packs, metas, format)
	if err != nil {
		return nil, err
	}
	if err := writeFile(out, "pack.mcmeta", meta); err != nil {
		return nil, err
	}
	hidden := map[[2]int][]string{}
	for _, name := range names {
		have := files[name]
		if name == "pack.mcmeta" {
			continue
		}
		// What a texture is drawn with, beside it, is of the pack the
		// texture is from, or one over it: one under has none of its say.
		if asset, ok := files[strings.TrimSuffix(name, ".mcmeta")]; ok && strings.HasSuffix(name, ".mcmeta") {
			for _, e := range have {
				if e.pack <= asset[0].pack {
					if err := copyFile(out, name, e.file); err != nil {
						return nil, err
					}
					break
				}
			}
			continue
		}
		if len(have) == 1 || name == "pack.png" {
			if err := copyFile(out, name, have[0].file); err != nil {
				return nil, err
			}
			continue
		}
		if kind := kindOf(name); kind != plain {
			data, err := mergeJSON(kind, have)
			if err == nil {
				if err := writeFile(out, name, data); err != nil {
					return nil, err
				}
				continue
			}
			if !errors.Is(err, errCannotJoin) {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		// The top one's, and any below that is not the same is hidden.
		if err := copyFile(out, name, have[0].file); err != nil {
			return nil, err
		}
		for _, under := range have[1:] {
			if !sameFile(have[0].file, under.file) {
				pair := [2]int{have[0].pack, under.pack}
				hidden[pair] = append(hidden[pair], name)
			}
		}
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	res := &Result{}
	for pair, files := range hidden {
		res.Hidden = append(res.Hidden, Hidden{Over: pair[0], Under: pair[1], Files: files})
	}
	sort.Slice(res.Hidden, func(i, j int) bool {
		a, b := res.Hidden[i], res.Hidden[j]
		return a.Over < b.Over || (a.Over == b.Over && a.Under < b.Under)
	})
	return res, nil
}

// packMeta is what is read of a pack's pack.mcmeta.
type packMeta struct {
	Pack     map[string]json.RawMessage `json:"pack"`
	Overlays struct {
		Entries []json.RawMessage `json:"entries"`
	} `json:"overlays"`
	Filter struct {
		Block []json.RawMessage `json:"block"`
	} `json:"filter"`
	Language map[string]json.RawMessage `json:"language"`
}

// readMeta reads a pack's pack.mcmeta; one with none says nothing.
func readMeta(p Pack) (packMeta, error) {
	var meta packMeta
	for _, f := range p.Zip.File {
		if name, ok := cleanName(f.Name); !ok || name != "pack.mcmeta" {
			continue
		}
		data, err := readFile(f)
		if err != nil {
			return meta, fmt.Errorf("pack.mcmeta of %s: %w", p.Name, err)
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			return meta, fmt.Errorf("pack.mcmeta of %s is not valid: %w", p.Name, err)
		}
		return meta, nil
	}
	return meta, nil
}

// filesOf is a pack's files, by name, as a game of this format reads them:
// the overlays it uses laid over the rest, the later ones on top. With no
// format, as they are.
func filesOf(p Pack, meta packMeta, format int) map[string]*zip.File {
	root := map[string]*zip.File{}
	for _, f := range p.Zip.File {
		name, ok := cleanName(f.Name)
		// A zip with the same name twice: its first is the one read.
		if _, seen := root[name]; !ok || seen || f.FileInfo().IsDir() {
			continue
		}
		root[name] = f
	}
	if format == 0 {
		return root
	}
	var every, used []string
	for _, raw := range meta.Overlays.Entries {
		var o struct {
			Directory string `json:"directory"`
		}
		if json.Unmarshal(raw, &o) != nil || o.Directory == "" || strings.Contains(o.Directory, "/") {
			continue
		}
		every = append(every, o.Directory+"/")
		if overlayFits(raw, format) {
			used = append(used, o.Directory+"/")
		}
	}
	out := map[string]*zip.File{}
	for name, f := range root {
		if !hasAnyPrefix(name, every) {
			out[name] = f
		}
	}
	for _, dir := range used {
		for name, f := range root {
			if rest, ok := strings.CutPrefix(name, dir); ok && rest != "" && rest != "pack.mcmeta" {
				out[rest] = f
			}
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// overlayFits says whether an overlay is for this format: by formats (a
// number, [min, max], or {min_inclusive, max_inclusive}), or from the
// game's 1.21.9 on by min_format and max_format.
func overlayFits(raw json.RawMessage, format int) bool {
	var o struct {
		Formats   json.RawMessage `json:"formats"`
		MinFormat json.RawMessage `json:"min_format"`
		MaxFormat json.RawMessage `json:"max_format"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return false
	}
	if o.Formats != nil {
		lo, hi, ok := formatRange(o.Formats)
		return ok && lo <= format && format <= hi
	}
	lo, okLo := major(o.MinFormat)
	hi, okHi := major(o.MaxFormat)
	return okLo && okHi && lo <= format && format <= hi
}

func formatRange(v json.RawMessage) (int, int, bool) {
	if n, ok := major(v); ok {
		return n, n, true
	}
	var pair []int
	if json.Unmarshal(v, &pair) == nil && len(pair) == 2 {
		return pair[0], pair[1], true
	}
	var bounds struct {
		Min *int `json:"min_inclusive"`
		Max *int `json:"max_inclusive"`
	}
	if json.Unmarshal(v, &bounds) == nil && bounds.Min != nil && bounds.Max != nil {
		return *bounds.Min, *bounds.Max, true
	}
	return 0, 0, false
}

// major is a format as a number, or the major of [major] or [major, minor].
func major(v json.RawMessage) (int, bool) {
	var n int
	if json.Unmarshal(v, &n) == nil {
		return n, true
	}
	var parts []int
	if json.Unmarshal(v, &parts) == nil && len(parts) > 0 {
		return parts[0], true
	}
	return 0, false
}

// hideFiltered takes out of a pack's files those a pack over it filters
// out: assets whose namespace and path match a block filter's patterns,
// each the whole of it, a pattern left out matching any.
func hideFiltered(files map[string]*zip.File, blocks []json.RawMessage) {
	for _, raw := range blocks {
		var b struct {
			Namespace *string `json:"namespace"`
			Path      *string `json:"path"`
		}
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		match := func(pattern *string) func(string) bool {
			if pattern == nil {
				return func(string) bool { return true }
			}
			re, err := regexp.Compile(`^(?:` + *pattern + `)$`)
			if err != nil {
				// A pattern Go does not read is not applied.
				return func(string) bool { return false }
			}
			return re.MatchString
		}
		ns, path := match(b.Namespace), match(b.Path)
		for name := range files {
			rest, ok := strings.CutPrefix(name, "assets/")
			if !ok {
				continue
			}
			space, p, ok := strings.Cut(rest, "/")
			if ok && ns(space) && path(p) {
				delete(files, name)
			}
		}
	}
}

type entry struct {
	pack int
	file *zip.File
}

// cleanName is a file's name in a pack as the game reads it, or false for
// one it would not read, and that has no place in a zip made.
func cleanName(name string) (string, bool) {
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." {
			return "", false
		}
	}
	return name, true
}

// sameFile says whether two files in packs are the same, as far as their
// zips say without reading them.
func sameFile(a, b *zip.File) bool {
	return a.CRC32 == b.CRC32 && a.UncompressedSize64 == b.UncompressedSize64
}

// copyFile copies a file from a pack as it is, without unpacking it.
func copyFile(out *zip.Writer, name string, f *zip.File) error {
	h := f.FileHeader
	h.Name, h.Modified, h.Extra, h.Comment = name, stamp, nil, ""
	h.ModifiedDate, h.ModifiedTime = 0, 0
	w, err := out.CreateRaw(&h)
	if err != nil {
		return err
	}
	r, err := f.OpenRaw()
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	return err
}

func writeFile(out *zip.Writer, name string, data []byte) error {
	w, err := out.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: stamp})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// readFile reads a file of a pack, up to jsonLimit.
func readFile(f *zip.File) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, jsonLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > jsonLimit {
		return nil, errCannotJoin
	}
	return data, nil
}

// mergeMeta makes the pack.mcmeta of the pack made: the format it is for,
// and every pack's block filters and languages. Their overlays are laid over
// their files already, for a format given; for none, they are kept, the
// bottom pack's first.
func mergeMeta(packs []Pack, metas []packMeta, format int) ([]byte, error) {
	names := make([]string, len(packs))
	for i, p := range packs {
		names[i] = p.Name
	}
	pack := map[string]any{"description": strings.Join(names, ", ")}
	var overlays, filters []json.RawMessage
	languages := map[string]json.RawMessage{}
	for i := len(metas) - 1; i >= 0; i-- {
		if format == 0 {
			overlays = append(overlays, metas[i].Overlays.Entries...)
		}
		filters = append(filters, metas[i].Filter.Block...)
		for k, v := range metas[i].Language {
			languages[k] = v
		}
	}
	switch {
	case format == 0:
		// With no format to say, what the top pack says.
		for _, k := range []string{"pack_format", "supported_formats", "min_format", "max_format"} {
			if v, ok := metas[0].Pack[k]; ok {
				pack[k] = v
			}
		}
	// From the game's 1.21.9, whose packs are of format 65 on, a pack says
	// the formats it is for as min_format and max_format.
	case format >= 65:
		pack["min_format"], pack["max_format"] = format, format
	default:
		pack["pack_format"] = format
	}
	meta := map[string]any{"pack": pack}
	if len(overlays) > 0 {
		meta["overlays"] = map[string]any{"entries": overlays}
	}
	if len(filters) > 0 {
		meta["filter"] = map[string]any{"block": filters}
	}
	if len(languages) > 0 {
		meta["language"] = languages
	}
	return json.MarshalIndent(meta, "", "  ")
}

// kind is how a file is joined.
type kind int

const (
	plain kind = iota
	sounds
	lang
	font
	atlas
	itemModel
	itemDefinition
)

// joined are the files that are joined, by where they are in a pack, also
// in an overlay's folder.
var joined = regexp.MustCompile(`^(?:[^/]+/)?assets/[^/]+/(sounds\.json|lang/[^/]+\.json|font/.+\.json|atlases/.+\.json|models/item/.+\.json|items/.+\.json)$`)

func kindOf(name string) kind {
	m := joined.FindStringSubmatch(name)
	if m == nil {
		return plain
	}
	switch rest := m[1]; {
	case rest == "sounds.json":
		return sounds
	case strings.HasPrefix(rest, "lang/"):
		return lang
	case strings.HasPrefix(rest, "font/"):
		return font
	case strings.HasPrefix(rest, "atlases/"):
		return atlas
	case strings.HasPrefix(rest, "models/item/"):
		return itemModel
	default:
		return itemDefinition
	}
}

// errCannotJoin is for files of a kind that is joined that cannot be: the
// top one is used, and those below are hidden.
var errCannotJoin = errors.New("cannot be joined")

type object = map[string]json.RawMessage

// mergeJSON joins the same file of several packs, the top one first.
func mergeJSON(k kind, have []entry) ([]byte, error) {
	objects := make([]object, len(have))
	for i, e := range have {
		data, err := readFile(e.file)
		if err != nil {
			return nil, err
		}
		// A file the game could not read either is not joined.
		if json.Unmarshal(data, &objects[i]) != nil || objects[i] == nil {
			return nil, errCannotJoin
		}
	}
	// From the bottom up, each over what is below it.
	out := objects[len(objects)-1]
	for i := len(objects) - 2; i >= 0; i-- {
		var err error
		if out, err = mergeTwo(k, out, objects[i]); err != nil {
			return nil, err
		}
	}
	return json.MarshalIndent(out, "", "  ")
}

// mergeTwo joins the same file of two packs, under below over.
func mergeTwo(k kind, under, over object) (object, error) {
	switch k {
	case lang:
		return mergeKeys(under, over), nil
	case sounds:
		return mergeSounds(under, over)
	case font:
		// The first provider that has a character draws it: the top
		// pack's come first.
		return joinList(over, "providers", over, under)
	case atlas:
		// The game joins atlases' sources from every pack, the bottom
		// one's first.
		return joinList(over, "sources", under, over)
	case itemModel:
		// The game does not join these: the top pack's model, with the
		// overrides of both, the last of which that fits is used.
		return joinList(over, "overrides", under, over)
	case itemDefinition:
		return mergeItem(under, over)
	}
	return nil, errCannotJoin
}

// mergeKeys has every key of both, those of over where both have one.
func mergeKeys(under, over object) object {
	out := object{}
	for k, v := range under {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// joinList is base, with its list of key that of first and then that of
// second.
func joinList(base object, key string, first, second object) (object, error) {
	var a, b []json.RawMessage
	if v, ok := first[key]; ok && json.Unmarshal(v, &a) != nil {
		return nil, errCannotJoin
	}
	if v, ok := second[key]; ok && json.Unmarshal(v, &b) != nil {
		return nil, errCannotJoin
	}
	out := mergeKeys(object{}, base)
	delete(out, key)
	if len(a)+len(b) > 0 {
		list, err := json.Marshal(append(a, b...))
		if err != nil {
			return nil, err
		}
		out[key] = list
	}
	return out, nil
}

// mergeSounds joins two sounds.json, event by event, as the game does: an
// event in the one over that says replace is its alone, and otherwise its
// sounds are added to those of the one under.
func mergeSounds(under, over object) (object, error) {
	out := mergeKeys(object{}, under)
	for name, v := range over {
		below, ok := under[name]
		if !ok {
			out[name] = v
			continue
		}
		var a, b object
		if json.Unmarshal(below, &a) != nil || json.Unmarshal(v, &b) != nil {
			return nil, errCannotJoin
		}
		var replace bool
		if r, ok := b["replace"]; ok && json.Unmarshal(r, &replace) == nil && replace {
			out[name] = v
			continue
		}
		// Its sounds are added to the one under's, which is otherwise as it
		// was: its subtitle, or none, and whether it replaces the game's.
		event, err := joinList(a, "sounds", a, b)
		if err != nil {
			return nil, err
		}
		if out[name], err = json.Marshal(event); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// mergeItem joins two item definitions, of the game's 1.21.4 on, where both
// pick a model by the same property: by its value, as custom_model_data, or
// by which of several it is. One that does over one that does not keeps the
// other as what it falls back to.
func mergeItem(under, over object) (object, error) {
	var a, b object
	if json.Unmarshal(under["model"], &a) != nil || json.Unmarshal(over["model"], &b) != nil {
		return nil, errCannotJoin
	}
	model, err := mergeItemModel(a, b)
	if err != nil {
		return nil, err
	}
	out := mergeKeys(under, over)
	if out["model"], err = json.Marshal(model); err != nil {
		return nil, err
	}
	return out, nil
}

func mergeItemModel(under, over object) (object, error) {
	ta, tb := typeOf(under), typeOf(over)
	// Only a pick of which custom item it is: by its custom model data, or
	// by a component such as its name. Others, as by where it is held, are
	// how one pack draws it, and the top one's is used.
	picks := func(o object) bool {
		var property string
		_ = json.Unmarshal(o["property"], &property)
		property = namespaced(property)
		t := typeOf(o)
		return (t == "minecraft:range_dispatch" || t == "minecraft:select") && (property == "minecraft:custom_model_data" || (t == "minecraft:select" && property == "minecraft:component"))
	}
	switch {
	case picks(under) && picks(over) && ta == tb && sameExcept(under, over, "entries", "cases", "fallback"):
		out := mergeKeys(object{}, over)
		if ta == "minecraft:range_dispatch" {
			entries, err := mergeBy(under["entries"], over["entries"], "threshold")
			if err != nil {
				return nil, err
			}
			out["entries"] = entries
		} else {
			cases, err := mergeCases(under["cases"], over["cases"])
			if err != nil {
				return nil, err
			}
			out["cases"] = cases
		}
		if _, ok := over["fallback"]; !ok {
			if f, ok := under["fallback"]; ok {
				out["fallback"] = f
			}
		}
		return out, nil
	case picks(over) && !picks(under):
		// The one over picks; what it falls back to, if it says none, is
		// what the one under looks like.
		out := mergeKeys(object{}, over)
		if _, ok := over["fallback"]; !ok {
			fallback, err := json.Marshal(under)
			if err != nil {
				return nil, err
			}
			out["fallback"] = fallback
		}
		return out, nil
	case picks(under) && !picks(over):
		// The one over is what the item looks like otherwise.
		out := mergeKeys(object{}, under)
		fallback, err := json.Marshal(over)
		if err != nil {
			return nil, err
		}
		out["fallback"] = fallback
		return out, nil
	}
	return nil, errCannotJoin
}

// typeOf is a model's type, with its namespace, which may be left out.
func typeOf(o object) string {
	var t string
	_ = json.Unmarshal(o["type"], &t)
	return namespaced(t)
}

// namespaced is an id with its namespace, minecraft where it says none.
func namespaced(id string) string {
	if id != "" && !strings.Contains(id, ":") {
		return "minecraft:" + id
	}
	return id
}

// sameExcept says whether two objects are the same but for these keys,
// with what names a type, property or component the same with or without
// its namespace, as the game reads them.
func sameExcept(a, b object, except ...string) bool {
	strip := func(o object) map[string]any {
		out := map[string]any{}
		for k, v := range o {
			if !contains(except, k) {
				var x any
				_ = json.Unmarshal(v, &x)
				if id, ok := x.(string); ok && (k == "type" || k == "property" || k == "component") {
					x = namespaced(id)
				}
				out[k] = x
			}
		}
		return out
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// mergeBy joins two lists of objects told apart by key: over's first, and
// those of under that over has none of the same key as after them.
func mergeBy(under, over json.RawMessage, key string) (json.RawMessage, error) {
	var a, b []object
	if under != nil && json.Unmarshal(under, &a) != nil {
		return nil, errCannotJoin
	}
	if over != nil && json.Unmarshal(over, &b) != nil {
		return nil, errCannotJoin
	}
	taken := map[string]bool{}
	for _, o := range b {
		taken[canonical(o[key])] = true
	}
	out := b
	for _, o := range a {
		if !taken[canonical(o[key])] {
			out = append(out, o)
		}
	}
	return json.Marshal(out)
}

// canonical is a JSON value written the same way however it was written.
func canonical(v json.RawMessage) string {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return string(bytes.TrimSpace(v))
	}
	data, _ := json.Marshal(x)
	return string(data)
}

// mergeCases joins the cases of two selects: over's, and under's for the
// values over has no case for. A select may not have two cases for the same
// value, and a case may be for several, so an under's case for some it
// has is kept for the rest.
func mergeCases(under, over json.RawMessage) (json.RawMessage, error) {
	var a, b []object
	if under != nil && json.Unmarshal(under, &a) != nil {
		return nil, errCannotJoin
	}
	if over != nil && json.Unmarshal(over, &b) != nil {
		return nil, errCannotJoin
	}
	values := func(when json.RawMessage) []json.RawMessage {
		var list []json.RawMessage
		if json.Unmarshal(when, &list) == nil {
			return list
		}
		return []json.RawMessage{when}
	}
	taken := map[string]bool{}
	for _, c := range b {
		for _, v := range values(c["when"]) {
			taken[canonical(v)] = true
		}
	}
	out := b
	for _, c := range a {
		var left []json.RawMessage
		for _, v := range values(c["when"]) {
			if !taken[canonical(v)] {
				left = append(left, v)
				taken[canonical(v)] = true
			}
		}
		if len(left) == 0 {
			continue
		}
		c = mergeKeys(object{}, c)
		var err error
		if len(left) == 1 {
			c["when"] = left[0]
		} else if c["when"], err = json.Marshal(left); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return json.Marshal(out)
}
