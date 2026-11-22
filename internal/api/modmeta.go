package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// What a mod or plugin file says it is, for working out whether a server
// has what another needs: the loader goes by these IDs, not by file names
// or where a file came from. A fork built from another mod's source keeps
// its ID, a renamed file is the same mod, and a mod can carry others inside
// it (jar-in-jar), which the loader loads as if they were files of their own.

// modMeta is what a jar says of itself.
type modMeta struct {
	// IDs are its own: its mod ID first (a plugin's name), then those it
	// says it provides besides, as another mod would know it by.
	IDs []string
	// Version is its own version, as it says.
	Version string
	// Nested are the jars inside it that the loader loads too.
	Nested []modMeta
	// Depends are the versions of other mods it needs, by their IDs: any
	// one of the ranges given for an ID will do.
	Depends map[string][]string
	// Forge says its ranges are Maven's, as Forge and NeoForge write them.
	Forge bool
}

// maxNested bounds how deep jars inside jars are read, and how large one
// read whole into memory may be.
const (
	maxNestedDepth = 3
	maxNestedJar   = 64 << 20
	maxMetaFile    = 1 << 20
)

// all is every mod ID it gives the server: its own and those of what is
// inside it.
func (m modMeta) all() []string {
	var out []string
	var walk func(modMeta)
	walk = func(m modMeta) {
		for _, id := range m.IDs {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		for _, n := range m.Nested {
			walk(n)
		}
	}
	walk(m)
	return out
}

// primary is its own mod ID: what a mod that needs it asks for.
func (m modMeta) primary() string {
	if len(m.IDs) > 0 {
		return m.IDs[0]
	}
	return ""
}

// versionOf is the version it gives the server of a mod ID: its own, or
// that of the jar inside it that is that mod.
func (m modMeta) versionOf(id string) (string, bool) {
	if slices.Contains(m.IDs, id) {
		return m.Version, true
	}
	for _, n := range m.Nested {
		if v, ok := n.versionOf(id); ok {
			return v, true
		}
	}
	return "", false
}

// readModMeta reads what a mod or plugin file says of itself: Fabric's
// fabric.mod.json, Quilt's quilt.mod.json, Forge's and NeoForge's
// mods.toml, a plugin's plugin.yml or paper-plugin.yml; and the same of
// the jars it carries, as fabric.mod.json's and quilt.mod.json's "jars"
// and Forge's META-INF/jarjar/metadata.json list them.
func readModMeta(zr *zip.Reader, kind avsysv1.ContentKind) modMeta {
	return readModMetaAt(zr, kind, 0)
}

func readModMetaAt(zr *zip.Reader, kind avsysv1.ContentKind, depth int) modMeta {
	read := func(name string) ([]byte, error) {
		f, err := zr.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxMetaFile))
	}
	var m modMeta
	var nested []string
	if kind == avsysv1.KindPlugin {
		if raw, err := firstOf(read, "paper-plugin.yml", "plugin.yml"); err == nil {
			var p struct {
				Name     any `json:"name"`
				Version  any `json:"version"`
				Provides any `json:"provides"`
			}
			if yaml.Unmarshal(raw, &p) == nil {
				if name := scalar(p.Name); name != "" {
					m.IDs = append(m.IDs, name)
				}
				m.Version = scalar(p.Version)
				for _, other := range stringsOf(p.Provides) {
					m.IDs = appendID(m.IDs, other)
				}
			}
		}
		return m
	}
	if raw, err := read("fabric.mod.json"); err == nil {
		var f struct {
			ID       string                     `json:"id"`
			Version  string                     `json:"version"`
			Provides []string                   `json:"provides"`
			Depends  map[string]json.RawMessage `json:"depends"`
			Jars     []struct {
				File string `json:"file"`
			} `json:"jars"`
		}
		// Some say "provides" or "depends" in a way the loader would not
		// read either: what can be read is.
		_ = json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &f)
		m.IDs = appendID(m.IDs, f.ID)
		for _, p := range f.Provides {
			m.IDs = appendID(m.IDs, p)
		}
		m.Version = f.Version
		for id, r := range f.Depends {
			if m.Depends == nil {
				m.Depends = map[string][]string{}
			}
			m.Depends[id] = rangesOf(r)
		}
		for _, j := range f.Jars {
			nested = append(nested, j.File)
		}
	} else if raw, err := read("quilt.mod.json"); err == nil {
		var q struct {
			Loader struct {
				ID       string            `json:"id"`
				Version  string            `json:"version"`
				Provides []json.RawMessage `json:"provides"`
				Depends  []json.RawMessage `json:"depends"`
				Jars     []string          `json:"jars"`
			} `json:"quilt_loader"`
		}
		_ = json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &q)
		m.IDs = appendID(m.IDs, q.Loader.ID)
		m.Version = q.Loader.Version
		for _, p := range q.Loader.Provides {
			var id string
			if json.Unmarshal(p, &id) != nil {
				var o struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(p, &o)
				id = o.ID
			}
			m.IDs = appendID(m.IDs, quiltID(id))
		}
		for _, d := range q.Loader.Depends {
			var id string
			var ranges []string
			if json.Unmarshal(d, &id) != nil {
				var o struct {
					ID       string          `json:"id"`
					Versions json.RawMessage `json:"versions"`
					Optional bool            `json:"optional"`
				}
				if json.Unmarshal(d, &o) != nil || o.Optional {
					continue
				}
				id, ranges = o.ID, rangesOf(o.Versions)
			}
			if id = quiltID(id); id != "" {
				if m.Depends == nil {
					m.Depends = map[string][]string{}
				}
				m.Depends[id] = ranges
			}
		}
		nested = q.Loader.Jars
	} else if raw, err := firstOf(read, "META-INF/neoforge.mods.toml", "META-INF/mods.toml"); err == nil {
		m.Forge = true
		t := forgeToml(raw)
		for i, mod := range t.mods {
			m.IDs = appendID(m.IDs, mod["modId"])
			if i == 0 {
				m.Version = mod["version"]
			}
		}
		if strings.Contains(m.Version, "${file.jarVersion}") {
			m.Version = ""
			if mf, err := read("META-INF/MANIFEST.MF"); err == nil {
				m.Version = manifestValue(mf, "Implementation-Version")
			}
		}
		for _, d := range t.deps {
			id := d["modId"]
			// Forge's "mandatory", NeoForge's "type".
			if id == "" || d["mandatory"] == "false" || (d["type"] != "" && !strings.EqualFold(d["type"], "required")) {
				continue
			}
			if m.Depends == nil {
				m.Depends = map[string][]string{}
			}
			if r := d["versionRange"]; r != "" {
				m.Depends[id] = append(m.Depends[id], r)
			} else if _, had := m.Depends[id]; !had {
				m.Depends[id] = nil
			}
		}
		if raw, err := read("META-INF/jarjar/metadata.json"); err == nil {
			var jj struct {
				Jars []struct {
					Path string `json:"path"`
				} `json:"jars"`
			}
			if json.Unmarshal(raw, &jj) == nil {
				for _, j := range jj.Jars {
					nested = append(nested, j.Path)
				}
			}
		}
	}
	if strings.Contains(m.Version, "${") {
		m.Version = ""
	}
	if depth >= maxNestedDepth {
		return m
	}
	for _, name := range nested {
		name = strings.TrimPrefix(path.Clean("/"+name), "/")
		f, err := zr.Open(name)
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxNestedJar+1))
		f.Close()
		if err != nil || len(raw) > maxNestedJar {
			continue
		}
		inner, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			continue
		}
		if n := readModMetaAt(inner, kind, depth+1); len(n.IDs) > 0 || len(n.Nested) > 0 {
			m.Nested = append(m.Nested, n)
		}
	}
	return m
}

// quiltID is a mod ID as Quilt may write it, "group:id", as the loader
// knows it: the ID.
func quiltID(id string) string {
	if _, after, ok := strings.Cut(id, ":"); ok {
		return after
	}
	return id
}

var modIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.\- ]{1,128}$`)

// appendID adds a mod ID once, if it is one.
func appendID(ids []string, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" || !modIDPattern.MatchString(id) || slices.Contains(ids, id) {
		return ids
	}
	return append(ids, id)
}

// stringsOf is a YAML value that is one string or a list of them.
func stringsOf(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s := scalar(x); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// rangesOf is a version range as fabric.mod.json and quilt.mod.json write
// one: a string, a list of them of which any will do, or Quilt's {"any":
// [...]}. One it cannot read is none: anything will do.
func rangesOf(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var some []string
	if json.Unmarshal(raw, &some) == nil {
		return some
	}
	var any struct {
		Any []string `json:"any"`
	}
	if json.Unmarshal(raw, &any) == nil && len(any.Any) > 0 {
		return any.Any
	}
	return nil
}

// forgeTOML is what is wanted of a mods.toml: each [[mods]], and each
// [[dependencies.<id>]], as their plain keys.
type forgeTOML struct {
	mods []map[string]string
	deps []map[string]string
}

var tomlPair = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|'([^']*)'|([^\s#]+))`)

// forgeToml reads the [[mods]] and [[dependencies.x]] tables of a Forge or
// NeoForge mods.toml: their keys whose values are strings, numbers or
// true or false. Values of the file's own top are not wanted.
func forgeToml(raw []byte) forgeTOML {
	var t forgeTOML
	var in map[string]string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			in = nil
			switch {
			case trimmed == "[[mods]]":
				in = map[string]string{}
				t.mods = append(t.mods, in)
			case strings.HasPrefix(trimmed, "[[dependencies."):
				in = map[string]string{}
				t.deps = append(t.deps, in)
			}
			continue
		}
		if in == nil {
			continue
		}
		if m := tomlPair.FindStringSubmatch(line); m != nil {
			v := m[2]
			switch {
			case m[3] != "":
				v = m[3]
			case m[4] != "":
				v = m[4]
			default:
				v = strings.ReplaceAll(strings.ReplaceAll(v, `\"`, `"`), `\\`, `\`)
			}
			if _, had := in[m[1]]; !had {
				in[m[1]] = v
			}
		}
	}
	// A mod's ID is in a table's own pairs; for a dependency, which mod it
	// is for is in its header, and its modId pair says the one needed.
	t.mods = slices.DeleteFunc(t.mods, func(m map[string]string) bool { return m["modId"] == "" })
	return t
}

// fits says a version is within any one of ranges, as the loader would
// read them: Fabric's and Quilt's (">=1.2 <2", "^1.2", "~1.2", "1.2.x",
// "*") or, for forge, Maven's ("[1.2,2)", "[1.2,)"). A range it cannot
// read, or a version it cannot compare, fits: the panel only says what it
// is sure of.
func fits(version string, ranges []string, forge bool) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, r := range ranges {
		ok, sure := fitsOne(version, r, forge)
		if ok || !sure {
			return true
		}
	}
	return false
}

func fitsOne(version, r string, forge bool) (ok, sure bool) {
	r = strings.TrimSpace(r)
	if forge {
		return fitsMaven(version, r)
	}
	if r == "" || r == "*" {
		return true, true
	}
	v, okv := parseVersion(version)
	if !okv {
		return false, false
	}
	for _, p := range strings.Fields(r) {
		ok, sure := fitsPredicate(v, p)
		if !sure {
			return false, false
		}
		if !ok {
			return false, true
		}
	}
	return true, true
}

// fitsPredicate is one of Fabric's version predicates.
func fitsPredicate(v semver, p string) (ok, sure bool) {
	op := ""
	for _, o := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(p, o) {
			op, p = o, strings.TrimPrefix(p, o)
			break
		}
	}
	if p == "*" {
		return true, true
	}
	// "1.2.x": any of 1.2.
	if wild := strings.TrimSuffix(strings.TrimSuffix(p, ".x"), ".X"); wild != p || strings.HasSuffix(p, ".*") {
		wild = strings.TrimSuffix(wild, ".*")
		w, ok := parseVersion(wild)
		if !ok || op != "" {
			return false, false
		}
		return v.hasPrefix(w), true
	}
	want, ok := parseVersion(p)
	if !ok {
		return false, false
	}
	c := v.compare(want)
	switch op {
	case "", "=":
		return c == 0, true
	case ">=":
		return c >= 0, true
	case "<=":
		return c <= 0, true
	case ">":
		return c > 0, true
	case "<":
		return c < 0, true
	case "^":
		// The same major version, from this one.
		return c >= 0 && v.part(0) == want.part(0), true
	case "~":
		// The same major and minor version, from this one.
		return c >= 0 && v.part(0) == want.part(0) && v.part(1) == want.part(1), true
	}
	return false, false
}

// fitsMaven is a Maven version range, as Forge and NeoForge read one: a
// bare version is any version at all.
func fitsMaven(version, r string) (ok, sure bool) {
	if r == "" || r == "*" {
		return true, true
	}
	if !strings.ContainsAny(r, "[(") {
		return true, true
	}
	v, okv := parseVersion(version)
	if !okv {
		return false, false
	}
	// Several ranges, any of which will do: "[1,2),[3,4)".
	for len(r) > 0 {
		r = strings.TrimLeft(r, " ,")
		if r == "" {
			break
		}
		end := strings.IndexAny(r, "])")
		if end < 0 || (r[0] != '[' && r[0] != '(') {
			return false, false
		}
		one := r[:end+1]
		r = r[end+1:]
		low, high, comma := strings.Cut(one[1:len(one)-1], ",")
		low, high = strings.TrimSpace(low), strings.TrimSpace(high)
		if !comma {
			// "[1.0]": that one.
			want, ok := parseVersion(low)
			if !ok {
				return false, false
			}
			if v.compare(want) == 0 {
				return true, true
			}
			continue
		}
		in := true
		if low != "" {
			want, ok := parseVersion(low)
			if !ok {
				return false, false
			}
			c := v.compare(want)
			in = in && (c > 0 || (c == 0 && one[0] == '['))
		}
		if high != "" {
			want, ok := parseVersion(high)
			if !ok {
				return false, false
			}
			c := v.compare(want)
			in = in && (c < 0 || (c == 0 && one[len(one)-1] == ']'))
		}
		if in {
			return true, true
		}
	}
	return false, true
}

// semver is a version as far as the panel compares them: numbers between
// dots, then what comes after a "-" (a pre-release, which comes before the
// release it leads to). What comes after "+" is not compared.
type semver struct {
	parts []string
	pre   string
	// hasPre is a "-" given, even with nothing after it: before any
	// pre-release of that version.
	hasPre bool
}

var leadingNumber = regexp.MustCompile(`^[0-9]+`)

func parseVersion(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if plus := strings.IndexByte(s, '+'); plus >= 0 {
		s = s[:plus]
	}
	var v semver
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		v.pre, v.hasPre = s[dash+1:], true
		s = s[:dash]
	}
	if s == "" || !leadingNumber.MatchString(s) {
		return v, false
	}
	v.parts = strings.Split(s, ".")
	return v, true
}

func (v semver) part(i int) string {
	if i < len(v.parts) {
		return v.parts[i]
	}
	return "0"
}

// hasPrefix says v starts with the parts of w.
func (v semver) hasPrefix(w semver) bool {
	for i := range w.parts {
		if comparePart(v.part(i), w.parts[i]) != 0 {
			return false
		}
	}
	return true
}

func (v semver) compare(w semver) int {
	for i := range max(len(v.parts), len(w.parts)) {
		if c := comparePart(v.part(i), w.part(i)); c != 0 {
			return c
		}
	}
	switch {
	case v.hasPre && !w.hasPre:
		return -1
	case !v.hasPre && w.hasPre:
		return 1
	case !v.hasPre:
		return 0
	}
	a, b := strings.Split(v.pre, "."), strings.Split(w.pre, ".")
	for i := range max(len(a), len(b)) {
		switch {
		case i >= len(a):
			return -1
		case i >= len(b):
			return 1
		}
		if c := comparePart(a[i], b[i]); c != 0 {
			return c
		}
	}
	return 0
}

// comparePart compares numbers as numbers and anything else as text; a
// number comes before text.
func comparePart(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}
