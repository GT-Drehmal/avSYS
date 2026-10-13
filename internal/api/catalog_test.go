package api

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
)

func facetsOf(t *testing.T, params url.Values) [][]string {
	t.Helper()
	var facets [][]string
	if err := json.Unmarshal([]byte(params.Get("facets")), &facets); err != nil {
		t.Fatal(err)
	}
	return facets
}

// A search is narrowed to what fits the server, and further by whatever
// categories are asked for.
func TestModrinthSearchParams(t *testing.T) {
	got, err := modrinthSearchParams(url.Values{"type": {"mod"}, "loader": {"quilt"}, "gameVersion": {"1.21.4"}, "q": {"map"}})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"project_type:mod"}, {"categories:quilt", "categories:fabric"}, {"server_side:required", "server_side:optional"}, {"versions:1.21.4"}}
	if facets := facetsOf(t, got); !reflect.DeepEqual(facets, want) {
		t.Errorf("facets: got %v, want %v", facets, want)
	}
	if got.Get("index") != "relevance" || got.Get("query") != "map" || got.Get("offset") != "0" || got.Get("limit") != "24" {
		t.Errorf("defaults: %v", got)
	}

	got, err = modrinthSearchParams(url.Values{"type": {"mod"}, "loader": {"fabric"}, "sort": {"downloads"}, "categories": {"adventure,utility"}, "side": {"any"}, "offset": {"48"}})
	if err != nil {
		t.Fatal(err)
	}
	// Each category is a filter of its own, so a result has both.
	want = [][]string{{"project_type:mod"}, {"categories:fabric"}, {"categories:adventure"}, {"categories:utility"}}
	if facets := facetsOf(t, got); !reflect.DeepEqual(facets, want) {
		t.Errorf("facets: got %v, want %v", facets, want)
	}
	if got.Get("index") != "downloads" || got.Get("offset") != "48" {
		t.Errorf("order and offset: %v", got)
	}

	// Plugins and datapacks have no side to filter by.
	got, _ = modrinthSearchParams(url.Values{"type": {"plugin"}, "loader": {"paper"}})
	want = [][]string{{"project_type:plugin"}, {"categories:paper", "categories:spigot", "categories:bukkit"}}
	if facets := facetsOf(t, got); !reflect.DeepEqual(facets, want) {
		t.Errorf("plugin facets: got %v, want %v", facets, want)
	}

	for name, q := range map[string]url.Values{
		"no type":                       {},
		"an unknown type":               {"type": {"shader"}},
		"an unknown order":              {"type": {"mod"}, "sort": {"random"}},
		"a category that is a filter":   {"type": {"mod"}, "categories": {`adventure"],["project_type:shader`}},
		"a category with a colon in it": {"type": {"mod"}, "categories": {"versions:1.0"}},
	} {
		if _, err := modrinthSearchParams(q); err == nil {
			t.Errorf("%s was let through", name)
		}
	}
}

func TestCategoriesOf(t *testing.T) {
	body := []byte(`[
		{"name":"utility","project_type":"mod","header":"categories"},
		{"name":"adventure","project_type":"mod","header":"categories"},
		{"name":"quests","project_type":"modpack","header":"categories"},
		{"name":"16x","project_type":"resourcepack","header":"resolutions"},
		{"name":"themed","project_type":"resourcepack","header":"categories"},
		{"name":"Not A Tag","project_type":"mod","header":"categories"}
	]`)
	for tagType, want := range map[string][]string{
		"mod":          {"adventure", "utility"},
		"modpack":      {"quests"},
		"resourcepack": {"themed"},
		"shader":       {},
	} {
		got, err := categoriesOf(body, tagType)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v %v, want %v", tagType, got, err, want)
		}
	}
	if _, err := categoriesOf([]byte(`{"error":"x"}`), "mod"); err == nil {
		t.Error("an answer that is not a list was read as one")
	}
}
