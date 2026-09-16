package main

import (
	"maps"
	"slices"
	"testing"

	"github.com/ilyaluk/girabot/internal/gira"
)

func TestMigrateUserFavorites(t *testing.T) {
	// EMEL serial 1000101 and VAIMOO id 4555 are the same station, number 101.
	byNumber := map[string]gira.StationSerial{
		"101": "4555",
		"253": "4545",
	}

	favorites, dropped := migrateUserFavorites(map[gira.StationSerial]string{
		"1000101": "🏠",
		"1000253": "🏢",
		"1000999": "👻",
		"4700":    "already migrated",
	}, byNumber)

	want := map[gira.StationSerial]string{
		"4555": "🏠",
		"4545": "🏢",
		"4700": "already migrated",
	}
	if !maps.Equal(favorites, want) {
		t.Errorf("got %v, want %v", favorites, want)
	}

	// Station 999 is gone from the fleet, so its favorite cannot be kept.
	if !slices.Equal(dropped, []gira.StationSerial{"1000999"}) {
		t.Errorf("dropped = %v", dropped)
	}
}

func TestMigrateUserFavoritesOnEmpty(t *testing.T) {
	favorites, dropped := migrateUserFavorites(nil, map[string]gira.StationSerial{"101": "4555"})

	if len(favorites) != 0 || len(dropped) != 0 {
		t.Errorf("got %v, dropped %v", favorites, dropped)
	}
}
