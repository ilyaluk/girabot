package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/ilyaluk/girabot/internal/gira"
)

// legacyStationSerialPrefix is what EMEL prefixed station numbers with before
// the VAIMOO migration: station 101 had the serial 1000101.
const legacyStationSerialPrefix = "1000"

// migrateToVaimoo rewrites the identifiers saved per rider, which the VAIMOO
// migration changed the shape of. Stations are matched through the number both
// backends print in the station name; bikes and trips have nothing to match
// against, so a stale one is dropped.
func (s *server) migrateToVaimoo() {
	users, err := usersToMigrate(s.db)
	if err != nil {
		log.Println("error loading users for migration:", err)
		return
	}
	if len(users) == 0 {
		return
	}

	log.Printf("migrating saved state of %d users", len(users))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Stations are public, so this needs no rider's session.
	stations, err := gira.New(&http.Client{}, nil).GetStations(ctx)
	if err != nil {
		log.Println("error loading stations for migration:", err)
		return
	}

	byNumber := make(map[string]gira.StationSerial, len(stations))
	for _, station := range stations {
		byNumber[station.Number()] = station.Serial
	}

	for _, u := range users {
		favorites, dropped := migrateUserFavorites(u.Favorites, byNumber)
		for _, serial := range dropped {
			log.Printf("dropping favorite of station %s for %d: no such station any more", serial, u.ID)
		}
		u.Favorites = favorites

		// A bike callback now carries a communication id where it used to carry
		// an EMEL serial, and unlocking the old one would fail.
		u.LastSelectedBikeCb = ""

		// Trip codes are numeric under VAIMOO. An EMEL one can neither be rated
		// nor looked up, so the rating it is waiting for will never arrive.
		if _, err := strconv.ParseInt(string(u.CurrentTripCode), 10, 64); u.CurrentTripCode != "" && err != nil {
			log.Printf("dropping unrateable trip %s for %d", u.CurrentTripCode, u.ID)
			u.CurrentTripCode = ""
			u.CurrentTripRateAwaiting = false
			u.RateMessageID = ""
		}

		u.VaimooMigrated = true
		// Nothing else touches users yet, the bot has not started polling.
		if err := s.db.Save(&u).Error; err != nil {
			log.Printf("error saving migrated state for %d: %v", u.ID, err)
		}
	}
}

// usersToMigrate returns the riders whose saved state is still in EMEL shape.
//
// The flag column was added to a table that already had rows, and SQLite fills
// those with NULL rather than with the zero value of the type. NULL compares
// equal to nothing, so a plain "vaimoo_migrated = false" would match none of
// the users who actually need migrating.
func usersToMigrate(db *gorm.DB) ([]User, error) {
	var users []User
	err := db.Where("coalesce(vaimoo_migrated, 0) = 0").Find(&users).Error
	return users, err
}

// migrateUserFavorites rewrites one rider's favorites, returning the legacy
// serials it could not match to a station that still exists.
func migrateUserFavorites(
	favorites map[gira.StationSerial]string,
	byNumber map[string]gira.StationSerial,
) (map[gira.StationSerial]string, []gira.StationSerial) {
	res := make(map[gira.StationSerial]string, len(favorites))
	var dropped []gira.StationSerial

	for serial, name := range favorites {
		number, isLegacy := strings.CutPrefix(string(serial), legacyStationSerialPrefix)
		if !isLegacy {
			// Already a VAIMOO id, or something we don't recognise.
			res[serial] = name
			continue
		}

		migrated, ok := byNumber[number]
		if !ok {
			dropped = append(dropped, serial)
			continue
		}
		res[migrated] = name
	}

	return res, dropped
}
