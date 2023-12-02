package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tele "gopkg.in/telebot.v3"
	"gorm.io/gorm/clause"

	"girabot/internal/gira"
)

func (s *server) handleStart(c *customContext) error {
	if err := c.Send(messageHello); err != nil {
		return err
	}

	return s.handleLogin(c)
}

func (s *server) handleLogin(c *customContext) error {
	if err := c.Send(messageLogin); err != nil {
		return err
	}

	c.user.State = UserStateWaitingForEmail
	return nil
}

func (s *server) handleText(c *customContext) error {
	switch c.user.State {
	case UserStateWaitingForEmail:
		c.user.Email = c.Text()
		c.user.EmailMessageID = c.Message().ID

		if err := c.Send(messagePassword); err != nil {
			return err
		}
		c.user.State = UserStateWaitingForPassword
		return nil
	case UserStateWaitingForPassword:
		pwd := c.Text()
		m, err := s.bot.Send(c.Recipient(), "Logging in...")
		if err != nil {
			return err
		}

		tok, err := s.auth.Login(c.ctx, c.user.Email, pwd)
		if err != nil {
			return err
		}

		if err := s.bot.Delete(tele.StoredMessage{
			ChatID:    c.user.ID,
			MessageID: strconv.Itoa(c.user.EmailMessageID),
		}); err != nil {
			return err
		}
		if err := c.Delete(); err != nil {
			return err
		}

		c.user.Email = ""
		c.user.EmailMessageID = 0
		c.user.State = UserStateLoggedIn

		dbToken := Token{
			ID:    c.user.ID,
			Token: tok,
		}
		if err := s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&dbToken).Error; err != nil {
			return err
		}

		if err := s.handleStatus(c); err != nil {
			return err
		}

		if err := s.bot.Delete(m); err != nil {
			return err
		}

		return s.handleHelp(c)
	case UserStateLoggedIn:
		return c.Send("Unknown command, try /help")
	case UserStateWaitingForFavName:
		name := c.Text()
		if utf8.RuneCountInString(name) > 2 {
			return c.Send("Name too long, try again")
		}
		c.user.Favorites[c.user.EditingStationFav] = name
		c.user.EditingStationFav = ""
		c.user.State = UserStateLoggedIn
		return c.Send("Favorite renamed")
	case UserStateWaitingForRateComment:
		c.user.CurrentTripRating.Comment = c.Text()
		c.user.State = UserStateLoggedIn
		msg := tele.StoredMessage{
			ChatID:    c.user.ID,
			MessageID: c.user.CurrentTripMessageID,
		}
		// delete message with rating comment
		if err := c.Delete(); err != nil {
			return err
		}
		_, err := s.bot.Edit(
			msg,
			fmt.Sprintf(
				"Thanks for the comment! Don't forget to submit the rating.\n\n%s",
				c.user.CurrentTripRating.Comment,
			),
			getStarButtons(c.user.CurrentTripRating.Rating),
		)
		return err
	default:
		return c.Send("Unknown state")
	}
}

func (s *server) checkLoggedIn(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		cc := c.(*customContext)
		if cc.user.State < UserStateLoggedIn {
			return c.Send("Not logged in, use /start")
		}
		return next(c)
	}
}

func (s *server) handleHelp(c *customContext) error {
	return c.Send(messageHelp, tele.ModeMarkdown, menu)
}

func (s *server) handleFeedback(c *customContext) error {
	return c.Send(messageFeedback, tele.ModeMarkdown)
}

type UserState int

const (
	UserStateNone UserState = iota
	UserStateWaitingForEmail
	UserStateWaitingForPassword
	UserStateLoggedIn
	UserStateWaitingForFavName
	UserStateWaitingForRateComment
)

func (s *server) handleStatus(c *customContext) error {
	if err := c.Notify(tele.Typing); err != nil {
		return err
	}

	info, err := c.gira.GetClientInfo(c.ctx)
	if err != nil {
		return err
	}

	info.ActiveSubscriptions = slices.DeleteFunc(info.ActiveSubscriptions, func(i gira.ClientSubscription) bool {
		return !i.Active
	})

	subscr := "‼️ You don't have any active subscriptions. Please purchase one in official app."
	if len(info.ActiveSubscriptions) > 0 {
		subscr = "Active subscriptions:\n"
		for _, s := range info.ActiveSubscriptions {
			subscr += fmt.Sprintf(
				"• %s (until %s)\n",
				s.SubscriptionName,
				s.ExpirationDate.Format("2006-01-02"),
			)
		}
	}

	return c.Send(fmt.Sprintf(
		"Logged in. Gira account info:\nName: `%s`\nBalance: `%.2f€`\nBonus: `%d`\n%s",
		info.Name,
		info.Balance,
		info.Bonus,
		subscr,
	), tele.ModeMarkdown)
}

const (
	btnKeyTypeStation         = "station"
	btnKeyTypeStationNextPage = "next_stations"
	btnKeyTypeBike            = "bike"
	btnKeyTypeBikeUnlock      = "unlock_bike"

	btnKeyTypeCloseMenu = "close_menu"

	btnKeyTypeAddFav    = "add_favorite"
	btnKeyTypeRenameFav = "rename_favorite"
	btnKeyTypeRemoveFav = "remove_favorite"

	btnKeyTypeRateStar    = "rate_star"
	btnKeyTypeRateAddText = "rate_add_text"
	btnKeyTypeRateSubmit  = "rate_submit"
)

var (
	menu = &tele.ReplyMarkup{ResizeKeyboard: true}

	btnLocation  = menu.Location("📍 Send location")
	btnFavorites = menu.Text("⭐️ Show favorites")
	btnStatus    = menu.Text("ℹ️ Status")
	btnHelp      = menu.Text("❓ Help")
	btnFeedback  = menu.Text("📝 Feedback")
)

func init() {
	menu.Reply(
		menu.Row(btnLocation, btnFavorites),
		menu.Row(btnStatus, btnHelp, btnFeedback),
	)
}

func (s *server) handleLocationTest(c *customContext) error {
	return s.sendNearbyStations(c, &tele.Location{
		Lat: 38.725177,
		Lng: -9.149718,
	})
}

func (s *server) handleLocation(c *customContext) error {
	return s.sendNearbyStations(c, c.Message().Location)
}

func (s *server) sendNearbyStations(c *customContext, loc *tele.Location) error {
	err, cleanup := s.sendStationLoader(c)
	if err != nil {
		return err
	}
	defer cleanup()

	ss, err := c.gira.GetStations(c.ctx)
	if err != nil {
		return err
	}

	ss = slices.DeleteFunc(ss, func(i gira.Station) bool {
		return i.Status != gira.AssetStatusActive
	})

	slices.SortFunc(ss, func(i, j gira.Station) int {
		return cmp.Compare(distance(i, loc), distance(j, loc))
	})

	// do not store more than 50 stations
	ss = ss[:min(len(ss), stationMaxResults)]

	// store last search results to db for paging to work
	c.user.LastSearchLocation = loc
	c.user.LastSearchResults = make([]gira.StationSerial, len(ss))
	for i, s := range ss {
		c.user.LastSearchResults[i] = s.Serial
	}

	return s.sendStationList(c, ss[:min(stationPageSize, len(ss))], true, 5, loc)
}

func (s *server) sendStationLoader(c *customContext) (error, func()) {
	m, err := s.bot.Send(c.Recipient(), "Loading stations...")
	if err != nil {
		return err, nil
	}
	if err := c.Notify(tele.Typing); err != nil {
		return err, nil
	}
	return nil, func() {
		if err := s.bot.Delete(m); err != nil {
			log.Println("error deleting message:", err)
		}
	}
}

const (
	stationPageSize   = 5
	stationMaxResults = 50
)

// sendStationList sends a list of stations to the user.
// If loc is not nil, it will also show the distance to the station.
// Callers should not pass more than 5 stations at once.
func (s *server) sendStationList(c *customContext, stations []gira.Station, next bool, nextOff int, loc *tele.Location) error {
	stationsDocks := make([]gira.Docks, len(stations))
	wg := sync.WaitGroup{}
	wg.Add(len(stations))
	for i, s := range stations {
		go func(i int, s gira.StationSerial) {
			defer wg.Done()
			docks, err := c.gira.GetStationDocks(c.ctx, s)
			if err != nil {
				return
			}
			stationsDocks[i] = docks
		}(i, s.Serial)
	}
	wg.Wait()

	sb := strings.Builder{}
	rm := &tele.ReplyMarkup{}

	for i, s := range stations {
		var dist string
		if loc != nil {
			dist = fmt.Sprintf(" %.0fm", distance(s, loc))
		}

		var fav string
		if name := c.user.Favorites[s.Serial]; name != "" {
			fav = fmt.Sprintf("[%s] ", name)
		}

		sb.WriteString(fmt.Sprintf(
			"• %s*%s*:%s %s\n",
			strings.ReplaceAll(fav, "[", "\\["), // escape markdown link
			s.Number(),
			dist,
			s.Location(),
		))

		// apparently, these values are not always the same
		freeDocks := min(stationsDocks[i].FreeDocks(), s.Docks-s.Bikes)

		btnText := fmt.Sprintf(
			"%s%s: %2d ⚡️ %2d ⚙️ %d 🆓",
			fav,
			s.Number(),
			stationsDocks[i].ElectricBikesAvailable(),
			stationsDocks[i].ConventionalBikesAvailable(),
			freeDocks,
		)
		if c.user.CurrentTripCode != "" {
			// if user has active trip, show only free docks to ease visibility
			btnText = fmt.Sprintf("%s%s: %d 🆓", fav, s.Number(), freeDocks)
		}

		rm.InlineKeyboard = append(rm.InlineKeyboard, []tele.InlineButton{
			{
				Unique: btnKeyTypeStation,
				Text:   btnText,
				Data:   string(s.Serial),
			},
		})
	}

	var lastRow []tele.InlineButton
	if next {
		lastRow = append(lastRow, tele.InlineButton{
			Unique: btnKeyTypeStationNextPage,
			Text:   "More",
			Data:   fmt.Sprint(nextOff),
		})
	}
	lastRow = append(lastRow, tele.InlineButton{
		Unique: btnKeyTypeCloseMenu,
		Text:   "Close",
	})
	rm.InlineKeyboard = append(rm.InlineKeyboard, lastRow)

	return c.Reply(sb.String(), tele.NoPreview, tele.ModeMarkdown, rm)
}

// distance returns the distance in meters between the station and the location.
//
//goland:noinspection ALL
func distance(station gira.Station, location *tele.Location) float64 {
	// https://www.movable-type.co.uk/scripts/latlong.html
	lat1 := station.Latitude
	lon1 := station.Longitude
	lat2 := float64(location.Lat)
	lon2 := float64(location.Lng)

	const r = 6371e3           // metres
	φ1 := lat1 * math.Pi / 180 // φ, λ in radians
	φ2 := lat2 * math.Pi / 180
	Δφ := (lat2 - lat1) * math.Pi / 180
	Δλ := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(Δφ/2)*math.Sin(Δφ/2) +
		math.Cos(φ1)*math.Cos(φ2)*
			math.Sin(Δλ/2)*math.Sin(Δλ/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return r * c
}

func (s *server) handleStationNextPage(c *customContext) error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	off, _ := strconv.Atoi(cb.Data)

	res := c.user.LastSearchResults
	stationSerials := res[min(off, len(res)):min(off+stationPageSize, len(res))]

	if len(stationSerials) == 0 {
		return c.Send("No more stations (bug?)")
	}

	var ss []gira.Station
	for _, serial := range stationSerials {
		s, err := c.gira.GetStationCached(c.ctx, serial)
		if err != nil {
			return err
		}
		ss = append(ss, s)
	}

	return s.sendStationList(
		c, ss,
		off+stationPageSize < len(c.user.LastSearchResults), off+stationPageSize,
		c.user.LastSearchLocation,
	)
}

func (s *server) handleStation(c *customContext) error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	serial := gira.StationSerial(cb.Data)
	station, err := c.gira.GetStationCached(c.ctx, serial)
	if err != nil {
		return err
	}

	docks, err := c.gira.GetStationDocks(c.ctx, serial)
	if err != nil {
		return err
	}

	// filter out docks with no bike or not active
	docks = slices.DeleteFunc(docks, func(d gira.Dock) bool {
		return d.Bike == nil || d.Status != gira.AssetStatusActive
	})
	// order electric bikes first, then by dock number
	slices.SortFunc(docks, func(i, j gira.Dock) int {
		if i.Bike.Type != j.Bike.Type {
			if i.Bike.Type == gira.BikeTypeElectric {
				return -1
			}
			return 1
		}
		return cmp.Compare(i.Number, j.Number)
	})

	var dockBtns []tele.Btn
	for _, dock := range docks {
		bikeDesc := fmt.Sprintf("Dock %d; %s", dock.Number, dock.Bike.TextString())

		dockBtns = append(dockBtns, tele.Btn{
			Unique: btnKeyTypeBike,
			Text:   dock.PrettyString(),
			Data:   fmt.Sprintf("%s|%s", dock.Bike.Serial, bikeDesc),
		})
	}

	rm := &tele.ReplyMarkup{}

	btns := rm.Split(2, dockBtns)
	btns = append([]tele.Row{getStationFavButtons(c, station.Serial)}, btns...)
	btns = append(btns, tele.Row{{
		Text:   "Close",
		Unique: btnKeyTypeCloseMenu,
	}})
	rm.Inline(btns...)

	// send station location as main message with buttons of bikes
	return c.Send(&tele.Venue{
		Location: tele.Location{
			Lat: float32(station.Latitude),
			Lng: float32(station.Longitude),
		},
		Title: station.MapTitle(),
	}, rm)
}

func (s *server) handleTapBike(c *customContext) error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	bikeSerial, bikeDesc, _ := strings.Cut(cb.Data, "|")

	btnsRow := []tele.InlineButton{
		{
			Text:   "🔓 Unlock",
			Unique: btnKeyTypeBikeUnlock,
			Data:   bikeSerial,
		},
		{
			Text:   "❌ Cancel",
			Unique: btnKeyTypeCloseMenu,
		},
	}

	return c.Send(bikeDesc, &tele.ReplyMarkup{
		InlineKeyboard: [][]tele.InlineButton{btnsRow},
	})
}

func (s *server) handleUnlockBike(c *customContext) error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	if err := c.Edit("Unlocking bike..."); err != nil {
		return err
	}

	ok, err := c.gira.ReserveBike(c.ctx, gira.BikeSerial(cb.Data))
	if err != nil {
		return err
	}

	if !ok {
		return c.Edit("Bike can't be reserved, try again?")
	}

	ok, err = c.gira.StartTrip(c.ctx)
	if err != nil {
		return err
	}

	if !ok {
		return c.Edit("Bike can't be unlocked, try again?")
	}

	go func() {
		if err := s.watchActiveTrip(c); err != nil {
			s.bot.OnError(fmt.Errorf("watching active trip: %v", err), c)
		}
	}()

	return c.Edit(
		"Unlocked bike, waiting for trip to start.\n"+
			"It might take some time to physically unlock the bike.",
		&tele.ReplyMarkup{},
	)
}

func (s *server) deleteCallbackMessage(c *customContext) error {
	if c.Message().ReplyTo != nil && !c.Message().ReplyTo.Sender.IsBot {
		if err := s.bot.Delete(c.Message().ReplyTo); err != nil {
			return err
		}
	}

	return c.Delete()
}

// TODO: make active trips watch survive restart
func (s *server) watchActiveTrip(c *customContext) error {
	didStart := false
	for i := 0; i < 12; i++ {
		time.Sleep(5 * time.Second)

		err := s.updateActiveTrip(c)
		if errors.Is(err, gira.ErrNoActiveTrip) {
			continue
		}
		if err != nil {
			return err
		}

		didStart = true
		break
	}

	if !didStart {
		return c.Edit("Trip didn't start after a minute, will not watch it anymore.")
	}

	ticker := time.NewTicker(50*time.Second + time.Second*time.Duration(rand.Intn(20)))
	defer ticker.Stop()

	const toleratableErrors = 3
	errs := 0

	for range ticker.C {
		err := s.updateActiveTrip(c)
		if errors.Is(err, gira.ErrNoActiveTrip) {
			trip, err := c.gira.GetTrip(c.ctx, c.user.CurrentTripCode)
			if err != nil {
				return err
			}

			if err := c.Edit(fmt.Sprintf(
				"Trip ended, thanks for using GiraBot!\n"+
					"Duration: %s\n"+
					"Cost: %.0f€",
				trip.PrettyDuration(),
				trip.Cost,
			)); err != nil {
				return err
			}

			// TODO: pay for trip if not free

			return s.handleRate(c)
		}
		if err != nil {
			errs++
			if errs > toleratableErrors {
				return err
			}
			s.bot.OnError(fmt.Errorf("watching trip (err %d/%d): %v", errs, toleratableErrors, err), c)
			continue
		}
	}

	return nil
}

func (s *server) updateActiveTrip(c *customContext) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	trip, err := c.gira.GetActiveTrip(ctx)
	if err != nil {
		return err
	}
	log.Printf("[uid:%d] active trip: %+v", c.user.ID, trip)

	_ = c.user.CurrentTripCode // just for keeping reference
	if err := s.db.Model(c.user).Update("CurrentTripCode", trip.Code).Error; err != nil {
		return err
	}

	var cost string
	if trip.Cost != 0 {
		cost = fmt.Sprintf(", cost:  %.2f€", trip.Cost)
	}

	return c.Edit(fmt.Sprintf(
		"Active trip: duration ≥%s%s",
		trip.PrettyDuration(),
		cost,
	), tele.ModeMarkdown)
}

func (s *server) handleRate(c *customContext) error {
	if c.user.CurrentTripCode == "" {
		return c.Send("No last trip code, can't rate")
	}

	c.user.CurrentTripRating = gira.TripRating{}

	m, err := s.bot.Send(c.Recipient(), "Please rate the trip", getStarButtons(0))
	if err != nil {
		return err
	}

	c.user.CurrentTripMessageID = strconv.Itoa(m.ID)
	return nil
}

func (s *server) handleRateStar(c *customContext) error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	rating, err := strconv.Atoi(cb.Data)
	if err != nil {
		return err
	}

	if c.user.CurrentTripRating.Rating != rating {
		c.user.CurrentTripRating.Rating = rating
		if err := c.Edit(getStarButtons(rating)); err != nil {
			return err
		}
	}

	return c.Respond()
}

func getStarButtons(rating int) *tele.ReplyMarkup {
	rm := &tele.ReplyMarkup{}
	var btns []tele.Btn
	for i := 0; i < 5; i++ {
		text := "☆"
		if i < rating {
			text = "⭐️"
		}
		btns = append(btns, tele.Btn{
			Unique: btnKeyTypeRateStar,
			Text:   text,
			Data:   strconv.Itoa(i + 1),
		})
	}
	rm.Inline(
		btns,
		tele.Row{
			{
				Unique: btnKeyTypeRateAddText,
				Text:   "📝 Add comment",
			},
			{
				Unique: btnKeyTypeRateSubmit,
				Text:   "📤 Submit",
			},
		},
	)
	return rm
}

func (s *server) handleRateAddText(c *customContext) error {
	c.user.State = UserStateWaitingForRateComment
	return c.Edit("Please send your comment regarding the trip")
}

func (s *server) handleRateSubmit(c *customContext) error {
	if c.user.CurrentTripCode == "" {
		return c.Edit("No last trip code, can't submit rating")
	}
	if c.user.CurrentTripRating.Rating == 0 {
		return c.Edit("Please select some stars first")
	}

	ok, err := c.gira.RateTrip(c.ctx, c.user.CurrentTripCode, c.user.CurrentTripRating)
	if err != nil {
		return err
	}
	if !ok {
		return c.Edit("Can't rate trip, try again?")
	}

	stars := strings.Repeat("⭐️", c.user.CurrentTripRating.Rating) + strings.Repeat("☆", 5-c.user.CurrentTripRating.Rating)
	var comment string
	if c.user.CurrentTripRating.Comment != "" {
		comment = fmt.Sprintf("\nComment: %s", c.user.CurrentTripRating.Comment)
	}

	c.user.CurrentTripMessageID = ""
	c.user.CurrentTripCode = ""
	c.user.CurrentTripRating = gira.TripRating{}

	if err := c.Edit(
		fmt.Sprint("Rating submitted, thanks!\n", stars, comment),
		&tele.ReplyMarkup{},
	); err != nil {
		return err
	}

	if !c.user.SentDonateMessage {
		if err := c.Send(messageDonate, tele.ModeMarkdown, tele.NoPreview); err != nil {
			return err
		}
		c.user.SentDonateMessage = true
	}

	return nil
}

func (s *server) handleAddFavorite(c *customContext) error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	if len(c.user.Favorites) >= stationMaxResults {
		return c.Send("Too many favorites, remove some first")
	}

	serial := gira.StationSerial(cb.Data)
	c.user.Favorites[serial] = "⭐️"

	if err := updateStationMsgFavoriteButtons(c, serial); err != nil {
		return err
	}

	return c.Respond(&tele.CallbackResponse{Text: "Added to favorites"})
}

func (s *server) handleRemoveFavorite(c *customContext) error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	serial := gira.StationSerial(cb.Data)
	delete(c.user.Favorites, serial)

	if err := updateStationMsgFavoriteButtons(c, serial); err != nil {
		return err
	}

	return c.Respond(&tele.CallbackResponse{Text: "Removed favorite"})
}

func updateStationMsgFavoriteButtons(c *customContext, serial gira.StationSerial) error {
	var favBtns []tele.InlineButton
	for _, btn := range getStationFavButtons(c, serial) {
		favBtns = append(favBtns, *btn.Inline())
	}

	rm := *c.Message().ReplyMarkup
	rm.InlineKeyboard[0] = favBtns
	return c.Edit(&rm)
}

func getStationFavButtons(c *customContext, serial gira.StationSerial) tele.Row {
	favRow := tele.Row{
		tele.Btn{
			Unique: btnKeyTypeAddFav,
			Text:   "⭐️ Add to favorites",
			Data:   string(serial),
		},
	}
	if name := c.user.Favorites[serial]; name != "" {
		favRow = tele.Row{
			tele.Btn{
				Unique: btnKeyTypeRenameFav,
				Text:   fmt.Sprintf("✏️ Rename [%s]", name),
				Data:   string(serial),
			},
			tele.Btn{
				Unique: btnKeyTypeRemoveFav,
				Text:   "❌ Remove fav",
				Data:   string(serial),
			},
		}
	}
	return favRow
}

func (s *server) handleRenameFavorite(c *customContext) error {
	if err := c.Send("Please send new name for this station (1-2 emojis tops)"); err != nil {
		return err
	}
	c.user.EditingStationFav = gira.StationSerial(c.Callback().Data)
	c.user.State = UserStateWaitingForFavName
	return nil
}

func (s *server) handleShowFavorites(c *customContext) error {
	if len(c.user.Favorites) == 0 {
		return c.Send("No favorites yet, add some from station view")
	}

	err, cleanup := s.sendStationLoader(c)
	if err != nil {
		return err
	}
	defer cleanup()

	var stations []gira.Station
	for serial := range c.user.Favorites {
		s, err := c.gira.GetStationCached(c.ctx, serial)
		if err != nil {
			return err
		}
		stations = append(stations, s)
	}

	slices.SortFunc(stations, func(i, j gira.Station) int {
		// first. compare by their label
		if c := cmp.Compare(c.user.Favorites[i.Serial], c.user.Favorites[j.Serial]); c != 0 {
			return c
		}
		// then, just by number
		return cmp.Compare(i.Number(), j.Number())
	})

	c.user.LastSearchLocation = nil
	c.user.LastSearchResults = make([]gira.StationSerial, len(stations))
	for i, s := range stations {
		c.user.LastSearchResults[i] = s.Serial
	}

	return s.sendStationList(
		c, stations[:min(stationPageSize, len(stations))],
		len(stations) > stationPageSize, 5,
		nil,
	)
}

func (s *server) handleDebug(c *customContext) error {
	handlers := map[string]func() (any, error){
		"user": func() (any, error) {
			return c.user, nil
		},
		"tokens": func() (any, error) {
			ts := s.getTokenSource(c.user.ID)
			tok, err := ts.Token()
			if err != nil {
				return nil, err
			}
			return *tok, nil
		},
		"client": func() (any, error) {
			return c.gira.GetClientInfo(c.ctx)
		},
		"stations": func() (any, error) {
			return c.gira.GetStations(c.ctx)
		},
		"station": func() (any, error) {
			return c.gira.GetStationDocks(c.ctx, gira.StationSerial(c.Args()[1]))
		},
		"stationByNumber": func() (any, error) {
			ss, err := c.gira.GetStations(c.ctx)
			if err != nil {
				return nil, err
			}
			for _, s := range ss {
				if s.Number() == c.Args()[1] {
					docks, err := c.gira.GetStationDocks(c.ctx, s.Serial)
					return map[string]any{
						"station": s,
						"docks":   docks,
					}, err
				}
			}
			return c.gira.GetStationDocks(c.ctx, gira.StationSerial(c.Args()[1]))
		},
		"activeTrip": func() (any, error) {
			return c.gira.GetActiveTrip(c.ctx)
		},
		"trip": func() (any, error) {
			return c.gira.GetTrip(c.ctx, gira.TripCode(c.Args()[1]))
		},
		"tripHistory": func() (any, error) {
			return c.gira.GetTripHistory(c.ctx)
		},
		"unratedTrips": func() (any, error) {
			return c.gira.GetUnratedTrips(c.ctx)
		},
		"doReserve": func() (any, error) {
			return c.gira.ReserveBike(c.ctx, gira.BikeSerial(c.Args()[1]))
		},
		"doCancel": func() (any, error) {
			return c.gira.CancelBikeReserve(c.ctx)
		},
		"doStart": func() (any, error) {
			return c.gira.StartTrip(c.ctx)
		},
		"doRateTrip": func() (any, error) {
			rating, _ := strconv.Atoi(c.Args()[2])
			req := gira.TripRating{
				Rating:  rating,
				Comment: c.Args()[3],
			}
			return c.gira.RateTrip(c.ctx, gira.TripCode(c.Args()[1]), req)
		},
		"doPayPoints": func() (any, error) {
			return c.gira.PayTripWithPoints(c.ctx, gira.TripCode(c.Args()[1]))
		},
		"doPayNoPoints": func() (any, error) {
			return c.gira.PayTripNoPoints(c.ctx, gira.TripCode(c.Args()[1]))
		},
		"wsServerTime": func() (any, error) {
			dur, err := time.ParseDuration(c.Args()[1])
			if err != nil {
				return nil, err
			}

			ctx, cancel := context.WithTimeout(context.Background(), dur)
			defer cancel()

			ch, err := gira.SubscribeServerDate(ctx, s.getTokenSource(c.user.ID))
			for t := range ch {
				_ = c.Send(fmt.Sprintf("Server time: %s", t.Format(time.RFC3339)))
			}

			return nil, err
		},
		"wsActiveTrip": func() (any, error) {
			dur, err := time.ParseDuration(c.Args()[1])
			if err != nil {
				return nil, err
			}

			ctx, cancel := context.WithTimeout(context.Background(), dur)
			defer cancel()

			ch, err := gira.SubscribeActiveTrips(ctx, s.getTokenSource(c.user.ID))
			for trip := range ch {
				_ = c.Send(fmt.Sprintf("Current trip: `%+v`", trip), tele.ModeMarkdown)
			}

			return nil, err
		},
	}

	help := func() error {
		var lines []string
		for k := range handlers {
			lines = append(lines, fmt.Sprintf("`/debug %s`\n\n", k))
		}
		slices.Sort(lines)
		res := "Invalid debug command. Options:\n\n" + strings.Join(lines, "")
		return c.Send(res, tele.ModeMarkdown)
	}

	if len(c.Args()) == 0 {
		return help()
	}

	handler, ok := handlers[c.Args()[0]]
	if !ok {
		return help()
	}

	val, err := handler()
	if err != nil {
		return err
	}

	valStr, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return err
	}

	const chunk = 4000
	for off := 0; off < len(valStr); off += chunk {
		end := off + chunk
		if end > len(valStr) {
			end = len(valStr)
		}
		if err := c.Send(
			fmt.Sprintf("```json\n%s```", valStr[off:end]),
			tele.ModeMarkdown,
		); err != nil {
			return err
		}
	}
	return nil
}
