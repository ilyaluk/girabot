package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"girabot/internal/giraauth"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	tele "gopkg.in/telebot.v3"
	"gorm.io/gorm/clause"

	"girabot/internal/gira"
)

func (c *customContext) handleStart() error {
	if err := c.Send(messageHello, tele.ModeMarkdown); err != nil {
		return err
	}

	return c.handleLogin()
}

func (c *customContext) handleLogin() error {
	if err := c.Send(messageLogin); err != nil {
		return err
	}

	c.user.State = UserStateWaitingForEmail
	return nil
}

func (c *customContext) handleText() error {
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
		m, err := c.Bot().Send(c.Recipient(), "Logging in...")
		if err != nil {
			return err
		}

		tok, err := c.s.auth.Login(c.ctx, c.user.Email, pwd)
		if errors.Is(err, giraauth.ErrInvalidCredentials) {
			_, err := c.Bot().Edit(
				m,
				"Invalid credentials, please try different password.\n"+
					"To change email, use /start again.",
			)
			return err
		}
		if err != nil {
			return err
		}

		if err := c.Bot().Delete(tele.StoredMessage{
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
		if err := c.s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&dbToken).Error; err != nil {
			return err
		}

		if err := c.handleStatus(); err != nil {
			return err
		}

		if err := c.Bot().Delete(m); err != nil {
			return err
		}

		return c.handleHelp()
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

		// delete message with rating comment
		if err := c.Delete(); err != nil {
			return err
		}
		_, err := c.Bot().Edit(
			c.getRateMsg(),
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

func (c *customContext) handleHelp() error {
	return c.Send(messageHelp, tele.ModeMarkdown, menu)
}

func (c *customContext) handleFeedback() error {
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

func (c *customContext) handleStatus() error {
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

	btnKeyTypeCloseMenu          = "close_menu"
	btnKeyTypeCloseMenuKeepReply = "close_menu_keep_reply"

	btnKeyTypeAddFav    = "add_favorite"
	btnKeyTypeRenameFav = "rename_favorite"
	btnKeyTypeRemoveFav = "remove_favorite"

	btnKeyTypeRateStar    = "rate_star"
	btnKeyTypeRateAddText = "rate_add_text"
	btnKeyTypeRateSubmit  = "rate_submit"

	btnKeyTypePayPoints = "trip_pay_points"
	btnKeyTypePayMoney  = "trip_pay_money"

	btnKeyTypeRetryDebug = "retry_debug"
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

func (c *customContext) handleLocationTest() error {
	return c.sendNearbyStations(&tele.Location{
		Lat: 38.725177,
		Lng: -9.149718,
	})
}

func (c *customContext) handleLocation() error {
	return c.sendNearbyStations(c.Message().Location)
}

func (c *customContext) sendNearbyStations(loc *tele.Location) error {
	err, cleanup := c.sendStationLoader()
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

	// do not store more than some reasonable amount of stations
	ss = ss[:min(len(ss), stationMaxResults)]

	// store last search results to db for paging to work
	c.user.LastSearchLocation = loc
	c.user.LastSearchResults = make([]gira.StationSerial, len(ss))
	for i, s := range ss {
		c.user.LastSearchResults[i] = s.Serial
	}

	return c.sendStationList(ss[:min(stationPageSize, len(ss))], true, 5, loc)
}

func (c *customContext) sendStationLoader() (error, func()) {
	m, err := c.Bot().Send(c.Recipient(), "Loading stations...")
	if err != nil {
		return err, nil
	}
	if err := c.Notify(tele.Typing); err != nil {
		return err, nil
	}
	return nil, func() {
		if err := c.Bot().Delete(m); err != nil {
			log.Println("error deleting message:", err)
		}
	}
}

const (
	stationPageSize   = 5
	stationMaxResults = 20
	stationMaxFaves   = 50
)

// sendStationList sends a list of stations to the user.
// If loc is not nil, it will also show the distance to the station.
// Callers should not pass more than 5 stations at once.
func (c *customContext) sendStationList(stations []gira.Station, next bool, nextOff int, loc *tele.Location) error {
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
			dist = fmt.Sprintf(" (_%.0fm_)", distance(s, loc))
		}

		var fav string
		if name := c.user.Favorites[s.Serial]; name != "" {
			fav = fmt.Sprintf("[%s] ", name)
		}

		sb.WriteString(fmt.Sprintf(
			"• %s*%s*%s: %s\n",
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
		if c.user.CurrentTripCode != "" && !c.user.CurrentTripRateAwaiting {
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

func (c *customContext) handleStationNextPage() error {
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

	return c.sendStationList(
		ss,
		off+stationPageSize < len(c.user.LastSearchResults), off+stationPageSize,
		c.user.LastSearchLocation,
	)
}

func (c *customContext) handleStation() error {
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
	btns = append([]tele.Row{c.getStationFavButtons(station.Serial)}, btns...)
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

func (c *customContext) handleTapBike() error {
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

	return c.Send(bikeDesc+"\n\nTapping 'Unlock' will start the trip.", &tele.ReplyMarkup{
		InlineKeyboard: [][]tele.InlineButton{btnsRow},
	})
}

func (c *customContext) handleUnlockBike() error {
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
		if err := c.watchActiveTrip(true); err != nil {
			c.Bot().OnError(fmt.Errorf("watching active trip: %v", err), c)
		}
	}()

	c.user.CurrentTripMessageID = strconv.Itoa(c.Message().ID)
	return c.Edit(
		"Unlocked bike, waiting for trip to start.\n"+
			"It might take some time to physically unlock the bike.",
		&tele.ReplyMarkup{},
	)
}

func (c *customContext) deleteCallbackMessageWithReply() error {
	if c.Message().ReplyTo != nil && !c.Message().ReplyTo.Sender.IsBot {
		if err := c.Bot().Delete(c.Message().ReplyTo); err != nil {
			return err
		}
	}

	return c.Delete()
}

func (c *customContext) deleteCallbackMessage() error {
	return c.Delete()
}

func (c *customContext) watchActiveTrip(isNewTrip bool) error {
	// not using c.Send/Edit/etc here and in callees as it might be called upon start while reloading active trips

	// probably no one should have trips longer than a day
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()

	ch, err := gira.SubscribeActiveTrips(ctx, c.getTokenSource())
	if err != nil {
		return err
	}

	// TODO: check for case with two bikes and fast return
	// TODO: cancel watch if trip did not start after some time

	if isNewTrip {
		// first channel pass -- wait for new trip
		if err := c.waitForTripStart(ch); err != nil {
			return err
		}
	}

	// second channel pass -- look for current trip updates
	for trip := range ch {
		log.Printf("[uid:%d] active trip update: %+v", c.user.ID, trip)

		if trip.Code != c.user.CurrentTripCode {
			// got update for some old trip
			continue
		}

		if err := c.updateActiveTripMessage(trip); err != nil {
			return err
		}

		if trip.Finished {
			log.Printf("[uid:%d] active trip finished: %+v", c.user.ID, trip)
			cancel()

			c.user.FinishedTrips++
			if err := c.s.db.Model(c.user).Update("FinishedTrips", c.user.FinishedTrips).Error; err != nil {
				return err
			}

			return c.handleSendRateMsg()
		}
	}

	return nil
}

// waitForTripStart reads TripUpdates from the channel until it finds the one
// that is not finished or canceled. It then updates the user's current trip code
// and sends the initial message.
func (c *customContext) waitForTripStart(ch <-chan gira.TripUpdate) error {
	for trip := range ch {
		log.Printf("[uid:%d] got some current trip: %+v", c.user.ID, trip)

		if trip.Finished || trip.Canceled {
			// got update for some old trip
			continue
		}

		log.Printf("[uid:%d] active trip started: %+v", c.user.ID, trip)

		c.user.CurrentTripCode = trip.Code
		if err := c.s.db.Model(c.user).Update("CurrentTripCode", trip.Code).Error; err != nil {
			return err
		}

		// found trip, update initial message
		return c.updateActiveTripMessage(trip)
	}
	return nil
}

func (c *customContext) updateActiveTripMessage(trip gira.TripUpdate) error {
	if trip.Error != 0 {
		return fmt.Errorf("active trip watch: %d", trip.Error)
	}

	if trip.Finished {
		var btns tele.Row

		if trip.Cost > 0 {
			log.Printf("last trip was not free: %+v", trip)

			if trip.CanUsePoints {
				btns = append(btns, tele.Btn{
					Unique: btnKeyTypePayPoints,
					Text:   "💰 Pay with points",
					Data:   string(trip.Code),
				})
			}

			if trip.CanPayWithMoney {
				btns = append(btns, tele.Btn{
					Unique: btnKeyTypePayMoney,
					Text:   "💶 Pay with money",
					Data:   string(trip.Code),
				})
			}
		}

		rm := &tele.ReplyMarkup{}
		rm.Inline(btns)

		if _, err := c.Bot().Send(
			tele.ChatID(c.user.ID),
			fmt.Sprintf(
				"Trip ended, thanks for using BetterGiraBot!\n"+
					"Bike: %s\n"+
					"Duration: %s\n"+
					"Cost: %.0f€\n"+
					"Points earned: %d (total %d)",
				trip.Bike,
				trip.PrettyDuration(),
				trip.Cost,
				trip.TripPoints,
				trip.ClientPoints,
			),
			rm,
		); err != nil {
			return err
		}

		if err := c.Bot().Delete(c.getActiveTripMsg()); err != nil {
			return err
		}
		c.user.CurrentTripMessageID = ""

		return nil
	}

	var costStr string
	if trip.Cost != 0 {
		costStr = fmt.Sprintf("\nCost:  %.2f€", trip.Cost)
	}

	_, err := c.Bot().Edit(
		c.getActiveTripMsg(),
		fmt.Sprintf(
			"Active trip:\nBike %s\nDuration ≥%s",
			trip.Bike,
			trip.PrettyDuration(),
		)+costStr, tele.ModeMarkdown)
	if errors.Is(err, tele.ErrSameMessageContent) {
		// if we got two updates at the same time, we might get this error from TG
		return nil
	}
	return err
}

func (c *customContext) handlePayPoints() error {
	if c.Callback() == nil {
		return c.Send("No callback")
	}

	tc := gira.TripCode(c.Callback().Data)
	if tc == "" {
		return c.Send("No trip code")
	}

	paid, err := c.gira.PayTripWithPoints(c.ctx, tc)
	if err != nil {
		return err
	}

	log.Printf("paid for %s with points: %d", tc, paid)

	return c.Reply(fmt.Sprintf("Paid with points: %v", paid))
}

func (c *customContext) handlePayMoney() error {
	if c.Callback() == nil {
		return c.Send("No callback")
	}

	tc := gira.TripCode(c.Callback().Data)
	if tc == "" {
		return c.Send("No trip code")
	}

	paid, err := c.gira.PayTripWithMoney(c.ctx, tc)
	if err != nil {
		return err
	}

	log.Printf("paid for %s with money: %d", tc, paid)

	return c.Reply(fmt.Sprintf("Paid with money: %v", paid))
}

func (c *customContext) handleSendRateMsg() error {
	// not using c.Send/Edit/etc as it might be called upon start while reloading active trips
	log.Printf("[uid:%d] sending rate message", c.user.ID)

	if c.user.CurrentTripCode == "" {
		return fmt.Errorf("no saved trip code, can't rate")
	}

	c.user.CurrentTripRating = gira.TripRating{}
	c.user.CurrentTripRateAwaiting = true

	m, err := c.Bot().Send(tele.ChatID(c.user.ID), "Please rate the trip\nDon't forget to submit", getStarButtons(0))
	if err != nil {
		return err
	}

	c.user.RateMessageID = strconv.Itoa(m.ID)

	// this function might not called with a saved hook (from watchActiveTrip), so we need to save the user manually
	return c.s.db.Model(c.user).
		Update("CurrentTripRating", "{}").
		Update("CurrentTripRateAwaiting", true).
		Update("RateMessageID", strconv.Itoa(m.ID)).
		Error
}

func (c *customContext) handleRateStar() error {
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

func (c *customContext) handleRateAddText() error {
	c.user.State = UserStateWaitingForRateComment
	return c.Edit("Please send your comment regarding the trip")
}

func (c *customContext) handleRateSubmit() error {
	if c.user.CurrentTripCode == "" {
		return c.Edit("No last trip code, can't submit rating")
	}
	if c.user.CurrentTripRating.Rating == 0 {
		return c.Edit("Please select some stars first", getStarButtons(0))
	}

	ok, err := c.gira.RateTrip(c.ctx, c.user.CurrentTripCode, c.user.CurrentTripRating)
	if err != nil {
		return err
	}
	if !ok {
		return c.Edit("Can't rate trip, try again?", getStarButtons(c.user.CurrentTripRating.Rating))
	}

	stars := strings.Repeat("⭐️", c.user.CurrentTripRating.Rating) + strings.Repeat("☆", 5-c.user.CurrentTripRating.Rating)
	var comment string
	if c.user.CurrentTripRating.Comment != "" {
		comment = fmt.Sprintf("\nComment: %s", c.user.CurrentTripRating.Comment)
	}

	c.user.RateMessageID = ""
	c.user.CurrentTripCode = ""
	c.user.CurrentTripRating = gira.TripRating{}
	c.user.CurrentTripRateAwaiting = false

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

func (c *customContext) handleAddFavorite() error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	if len(c.user.Favorites) >= stationMaxFaves {
		return c.Send("Too many favorites, remove some first")
	}

	serial := gira.StationSerial(cb.Data)
	c.user.Favorites[serial] = "⭐️"

	if err := c.updateStationMsgFavoriteButtons(serial); err != nil {
		return err
	}

	return c.Respond(&tele.CallbackResponse{Text: "Added to favorites"})
}

func (c *customContext) handleRemoveFavorite() error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	serial := gira.StationSerial(cb.Data)
	delete(c.user.Favorites, serial)

	if err := c.updateStationMsgFavoriteButtons(serial); err != nil {
		return err
	}

	return c.Respond(&tele.CallbackResponse{Text: "Removed favorite"})
}

func (c *customContext) updateStationMsgFavoriteButtons(serial gira.StationSerial) error {
	var favBtns []tele.InlineButton
	for _, btn := range c.getStationFavButtons(serial) {
		favBtns = append(favBtns, *btn.Inline())
	}

	rm := *c.Message().ReplyMarkup
	rm.InlineKeyboard[0] = favBtns
	return c.Edit(&rm)
}

func (c *customContext) getStationFavButtons(serial gira.StationSerial) tele.Row {
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

func (c *customContext) handleRenameFavorite() error {
	if err := c.Send("Please send new name for this station (1-2 emojis tops)"); err != nil {
		return err
	}
	c.user.EditingStationFav = gira.StationSerial(c.Callback().Data)
	c.user.State = UserStateWaitingForFavName
	return nil
}

func (c *customContext) handleShowFavorites() error {
	if len(c.user.Favorites) == 0 {
		return c.Send("No favorites yet, add some from station view")
	}

	err, cleanup := c.sendStationLoader()
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

	return c.sendStationList(
		stations[:min(stationPageSize, len(stations))],
		len(stations) > stationPageSize, 5,
		nil,
	)
}

func (c *customContext) handleDebug() error {
	return c.runDebug(c.Text())
}

func (c *customContext) handleDebugRetry() error {
	if c.Callback() == nil {
		return c.Send("No callback")
	}

	if c.Message().ReplyTo == nil {
		return c.Send("No reply, can't retry")
	}

	return c.runDebug(c.Message().ReplyTo.Text)
}

func (c *customContext) runDebug(text string) error {
	defer func() {
		if err := recover(); err != nil {
			log.Println("panic in debug handler:", err)
			_ = c.Send(fmt.Sprintf("Panic: %v", err))
		}
	}()

	// remove /debug command
	_, text, _ = strings.Cut(text, " ")
	// split args for easier parsing
	args := strings.Split(text, " ")
	log.Printf("running debug command: %+v", args)

	handlers := map[string]func() (any, error){
		"user": func() (any, error) {
			return c.user, nil
		},
		"tokens": func() (any, error) {
			ts := c.getTokenSource()
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
			if len(args) == 1 {
				return "missing station serial", nil
			}
			return c.gira.GetStationDocks(c.ctx, gira.StationSerial(args[1]))
		},
		"stationByNumber": func() (any, error) {
			if len(args) == 1 {
				return "missing station number", nil
			}
			ss, err := c.gira.GetStations(c.ctx)
			if err != nil {
				return nil, err
			}
			for _, s := range ss {
				if s.Number() == args[1] {
					docks, err := c.gira.GetStationDocks(c.ctx, s.Serial)
					return map[string]any{
						"station": s,
						"docks":   docks,
					}, err
				}
			}
			return c.gira.GetStationDocks(c.ctx, gira.StationSerial(args[1]))
		},
		"activeTrip": func() (any, error) {
			return c.gira.GetActiveTrip(c.ctx)
		},
		"trip": func() (any, error) {
			if len(args) == 1 {
				return "missing trip code", nil
			}
			return c.gira.GetTrip(c.ctx, gira.TripCode(args[1]))
		},
		"tripHistory": func() (any, error) {
			return c.gira.GetTripHistory(c.ctx)
		},
		"unratedTrips": func() (any, error) {
			return c.gira.GetUnratedTrips(c.ctx)
		},
		"doReserve": func() (any, error) {
			if len(args) == 1 {
				return "missing bike serial", nil
			}
			return c.gira.ReserveBike(c.ctx, gira.BikeSerial(args[1]))
		},
		"doCancel": func() (any, error) {
			return c.gira.CancelBikeReserve(c.ctx)
		},
		"doStart": func() (any, error) {
			return c.gira.StartTrip(c.ctx)
		},
		"doRateTrip": func() (any, error) {
			args := strings.SplitN(text, " ", 3)
			if len(args) < 3 {
				return "missing trip code, rating and comment", nil
			}
			rating, _ := strconv.Atoi(args[2])
			req := gira.TripRating{
				Rating:  rating,
				Comment: args[3],
			}
			return c.gira.RateTrip(c.ctx, gira.TripCode(args[1]), req)
		},
		"doPayPoints": func() (any, error) {
			if len(args) == 1 {
				return "missing trip code", nil
			}
			return c.gira.PayTripWithPoints(c.ctx, gira.TripCode(args[1]))
		},
		"doPayMoney": func() (any, error) {
			if len(args) == 1 {
				return "missing trip code", nil
			}
			return c.gira.PayTripWithMoney(c.ctx, gira.TripCode(args[1]))
		},
		"wsServerTime": func() (any, error) {
			if len(args) == 1 {
				return "missing duration", nil
			}

			dur, err := time.ParseDuration(args[1])
			if err != nil {
				return nil, err
			}

			ctx, cancel := context.WithTimeout(context.Background(), dur)
			defer cancel()

			ch, err := gira.SubscribeServerDate(ctx, c.getTokenSource())
			for t := range ch {
				_ = c.Send(fmt.Sprintf("Server time: %s", t.Format(time.RFC3339)))
			}

			return nil, err
		},
		"wsActiveTrip": func() (any, error) {
			if len(args) == 1 {
				return "missing duration", nil
			}

			dur, err := time.ParseDuration(args[1])
			if err != nil {
				return nil, err
			}

			ctx, cancel := context.WithTimeout(context.Background(), dur)
			defer cancel()

			ch, err := gira.SubscribeActiveTrips(ctx, c.getTokenSource())
			for trip := range ch {
				_ = c.Send(fmt.Sprintf("Current trip: `%+v`", trip), tele.ModeMarkdown)
			}

			return nil, err
		},
		"metrics": func() (any, error) {
			ms, _ := prometheus.DefaultGatherer.Gather()
			ms = slices.DeleteFunc(ms, func(i *dto.MetricFamily) bool {
				return !strings.HasPrefix(*i.Name, "gira")
			})
			res := map[string]any{}
			for _, m := range ms {
				res[*m.Name] = m.Metric[0].Counter.Value
			}
			return res, nil
		},
		"sql": func() (any, error) {
			args := strings.SplitN(text, " ", 2)
			if len(args) < 2 {
				return "missing query", nil
			}

			rows, err := c.s.db.Raw(args[1]).Rows()
			if err != nil {
				return nil, err
			}

			var res []map[string]any
			for rows.Next() {
				var row map[string]any
				if err := c.s.db.ScanRows(rows, &row); err != nil {
					return nil, err
				}
				res = append(res, row)
			}
			if len(res) == 1 {
				return res[0], nil
			}
			return res, nil
		},
	}

	replyTo := c.Message()
	if replyTo.ReplyTo != nil {
		// if this function was called as a retry callback, reply to the original message
		replyTo = replyTo.ReplyTo
	}

	rm := &tele.ReplyMarkup{
		InlineKeyboard: [][]tele.InlineButton{{
			{
				Unique: btnKeyTypeRetryDebug,
				Text:   "Retry",
			},
			{
				Unique: btnKeyTypeCloseMenuKeepReply,
				Text:   "Close",
			},
		}},
	}

	help := func() error {
		var lines []string
		for k := range handlers {
			lines = append(lines, fmt.Sprintf("`/debug %s`\n\n", k))
		}
		slices.Sort(lines)
		res := "Invalid debug command. Options:\n\n" + strings.Join(lines, "")
		_, err := c.Bot().Reply(replyTo, res, tele.ModeMarkdown, rm)
		return err
	}

	if len(args) == 0 {
		return help()
	}

	handler, ok := handlers[args[0]]
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
		if _, err := c.Bot().Reply(
			replyTo,
			fmt.Sprintf("```json\n%s```", valStr[off:end]),
			tele.ModeMarkdown,
			rm,
		); err != nil {
			return err
		}
	}
	return nil
}
