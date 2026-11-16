package controller

import (
	"reflect"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/mcproto"
)

// Who is on keeps each player's UUID beside their name, for their head, but
// not one the game hides or one that is not a UUID.
func TestPlayersOf(t *testing.T) {
	st := mcproto.Status{Players: mcproto.Players{Online: 5, Max: 20, Sample: []mcproto.Player{
		{Name: "Steve", ID: "8667BA71-B85A-4004-AF54-457A9734EED7"},
		{Name: "Alex", ID: "ec561538f3fd461daff5086b22154bce"},
		{Name: "Anonymous Player", ID: "00000000-0000-0000-0000-000000000000"},
		{Name: "§aWelcome!", ID: "not a uuid"},
		{Name: "Tim203", ID: "00000000-0000-0000-0009-01f4a1b2c3d4"},
	}}}
	got := playersOf(st)
	want := &avsysv1.PlayersStatus{
		Online: 5, Max: 20,
		Names: []string{"Steve", "Alex", "Anonymous Player", "§aWelcome!", "Tim203"},
		Sample: []avsysv1.OnlinePlayer{
			{Name: "Steve", ID: "8667ba71-b85a-4004-af54-457a9734eed7"},
			{Name: "Alex", ID: "ec561538f3fd461daff5086b22154bce"},
			{Name: "Anonymous Player"},
			{Name: "§aWelcome!"},
			// A Bedrock player, through Floodgate: GeyserMC has their head.
			{Name: "Tim203", ID: "00000000-0000-0000-0009-01f4a1b2c3d4"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("playersOf = %+v, want %+v", got, want)
	}

	// Nobody on is nobody named.
	if got := playersOf(mcproto.Status{Players: mcproto.Players{Max: 20}}); got.Names != nil || got.Sample != nil || got.Max != 20 {
		t.Errorf("playersOf with nobody on = %+v", got)
	}
}
