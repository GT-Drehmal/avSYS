package controller

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// listEntry is a player on one of the game's lists, as whitelist.json has
// them.
type listEntry struct{ UUID, Name string }

var imageUUID = regexp.MustCompile(`^(?i:[a-f0-9]{32}|[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})$`)

// imageTakes is what the image does with a list it is given as it starts
// (mc-image-helper's manage-users): a player already on the game's file, by
// UUID or by name, is left as they are; a UUID that is not is added as it
// is, with no name; a name that is not is looked up with Mojang, and one
// Mojang has no account of stops the start.
func imageTakes(given string, file []listEntry, mojang map[string]string) ([]listEntry, error) {
	out := slices.Clone(file)
	for _, p := range strings.Split(given, "\n") {
		if p == "" {
			continue
		}
		if imageUUID.MatchString(p) {
			if !slices.ContainsFunc(out, func(e listEntry) bool { return strings.EqualFold(e.UUID, p) }) {
				out = append(out, listEntry{UUID: p})
			}
			continue
		}
		if slices.ContainsFunc(out, func(e listEntry) bool { return strings.EqualFold(e.Name, p) }) {
			continue
		}
		uuid, ok := mojang[strings.ToLower(p)]
		if !ok {
			return nil, fmt.Errorf("Mojang has no player %s", p)
		}
		out = append(out, listEntry{UUID: uuid, Name: p})
	}
	return out, nil
}

// A Bedrock player kept in a server's settings does not stop it starting
// once they are gone from the game's file: kept by the UUID Floodgate gives
// them, as Settings keeps one, they are put back by it; kept by the name
// Floodgate gives them, as Settings once kept one, they are not given to
// the image, which would look the name up with Mojang.
func TestKeptBedrockPlayersDoNotStopAStart(t *testing.T) {
	const tim = "00000000-0000-0000-0009-01f64f65c7c3"
	steve := listEntry{UUID: "8667ba71-b85a-4004-af54-457a9734eed7", Name: "Steve"}
	mojang := map[string]string{"steve": steve.UUID, "alex": "ec561538-f3fd-461d-aff5-086b22154bce"}
	for name, kept := range map[string][]string{
		"by UUID":            {"Steve", tim, "Alex"},
		"by name":            {"Steve", ".Tim203", "Alex"},
		"by a prefix of its": {"Steve", "*Tim203", "Alex"},
	} {
		t.Run(name, func(t *testing.T) {
			env := envOf(server(func(mc *avsysv1.MinecraftServer) {
				mc.Spec.Whitelist = kept
				mc.Spec.Ops = kept
			}))
			for _, list := range []string{"WHITELIST", "OPS"} {
				// Tim203 was taken off in game: the file has only Steve.
				got, err := imageTakes(env[list], []listEntry{steve}, mojang)
				if err != nil {
					t.Fatalf("%s %q: the server does not start: %v", list, env[list], err)
				}
				if !slices.ContainsFunc(got, func(e listEntry) bool { return e.Name == "Alex" }) {
					t.Errorf("%s: Alex is not put on: %v", list, got)
				}
				if back := slices.ContainsFunc(got, func(e listEntry) bool { return e.UUID == tim }); back != (name == "by UUID") {
					t.Errorf("%s: Tim203 put back is %v: %v", list, back, got)
				}
			}
		})
	}
	// With none but Bedrock names, nothing is given.
	env := envOf(server(func(mc *avsysv1.MinecraftServer) { mc.Spec.Whitelist = []string{".Tim203"} }))
	if v, ok := env["WHITELIST"]; ok {
		t.Errorf("WHITELIST = %q, want none", v)
	}
}
