package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/mail"
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

	"github.com/ilyaluk/girabot/internal/firebasetoken"
	"github.com/ilyaluk/girabot/internal/gira"
	"github.com/ilyaluk/girabot/internal/giraauth"
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
	case UserStateNone:
		return c.handleStart()
	case UserStateWaitingForEmail:
		email := c.Text()
		emailParsed, err := mail.ParseAddress(email)
		if err != nil || emailParsed.Address != email {
			if err := c.Send("This does not look like valid email, please try again."); err != nil {
				return err
			}
			return c.deleteMessage(c.Message().ID)
		}

		c.user.Email = email
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

		tok, err := c.s.auth.Login(c, c.user.Email, pwd)
		if errors.Is(err, giraauth.ErrInvalidEmail) {
			if _, err := c.Bot().Edit(m, "Invalid email, please start over."); err != nil {
				return err
			}

			if err := c.deleteMessage(c.user.EmailMessageID); err != nil {
				return err
			}
			if err := c.Delete(); err != nil {
				return err
			}

			return c.handleLogin()
		}

		if errors.Is(err, giraauth.ErrInvalidCredentials) {
			if _, err := c.Bot().Edit(m,
				"Invalid credentials, please try different password.\n"+
					"To change email, run /login.",
			); err != nil {
				return err
			}

			return c.Delete()
		}
		if err != nil {
			return err
		}

		if err := c.deleteMessage(c.user.EmailMessageID); err != nil {
			return err
		}
		if err := c.Delete(); err != nil {
			return err
		}

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

		c.user.Email = ""
		c.user.EmailMessageID = 0
		c.user.State = UserStateLoggedIn

		return c.handleHelp()
	case UserStateLoggedIn:
		return c.handleLoggedInText()
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

		if err := c.Send("Thanks for the comment! Don't forget to submit the rating."); err != nil {
			return err
		}

		_, err := c.Bot().Edit(
			c.getRateMsg(),
			messageRateTrip,
			getStarButtons(c.user.CurrentTripRating.Rating),
		)
		return err
	default:
		return c.Send("Unknown state")
	}
}

func (c *customContext) deleteMessage(id int) error {
	return c.Bot().Delete(tele.StoredMessage{
		ChatID:    c.user.ID,
		MessageID: strconv.Itoa(id),
	})
}

func (s *server) checkLoggedIn(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		cc := c.(*customContext)
		if cc.user.State < UserStateLoggedIn {
			return c.Send("Not logged in, use /login")
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
	UserStateNone = UserState(iota)
	UserStateWaitingForEmail
	UserStateWaitingForPassword
	UserStateLoggedIn
	UserStateWaitingForFavName
	UserStateWaitingForRateComment
)

func (c *customContext) handleStatus() error {
	err, cleanup := c.sendTyping()
	if err != nil {
		return err
	}
	defer cleanup()

	info, err := c.gira.GetClientInfo(c)
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

	var balanceWarning string
	if info.Balance < 0 {
		balanceWarning = " ⚠️ _You won't be able to unlock bikes until you top up in official app._"
	}

	return c.Send(fmt.Sprintf(
		"Logged in. Gira account info:\n"+
			"Name: `%s`\n"+
			"Balance: `%.0f€`%s\n"+
			"Bonus: `%d` (`%d€`)\n"+
			"%s",
		info.Name,
		info.Balance,
		balanceWarning,
		info.Bonus,
		info.Bonus/500,
		subscr,
	), tele.ModeMarkdown)
}

const (
	btnKeyTypeStation    = "station"
	btnKeyTypeBike       = "bike"
	btnKeyTypeBikeUnlock = "unlock_bike"

	btnKeyTypeCloseMenu          = "close_menu"
	btnKeyTypeCloseMenuKeepReply = "close_menu_keep_reply"

	btnKeyTypeAddFav    = "add_favorite"
	btnKeyTypeRenameFav = "rename_favorite"
	btnKeyTypeRemoveFav = "remove_favorite"

	btnKeyTypeRateStar          = "rate_star"
	btnKeyTypeRateAddText       = "rate_add_text"
	btnKeyTypeRateCommentCancel = "rate_comment_cancel"
	btnKeyTypeRateSubmit        = "rate_submit"

	btnKeyTypePayPoints = "trip_pay_points"
	btnKeyTypePayMoney  = "trip_pay_money"

	btnKeyTypeRetryDebug = "retry_debug"

	btnKeyTypeIgnore = "ignore"
)

var (
	menu = &tele.ReplyMarkup{ResizeKeyboard: true}

	btnLocation  = menu.Location("📍 Location")
	btnMapLegacy = menu.Text("🗺️ Map")
	btnFavorites = menu.Text("⭐️ Favorites")
	btnStatus    = menu.Text("ℹ️ Status")
	btnHelp      = menu.Text("❓ Help")
	btnFeedback  = menu.Text("📝 Feedback")

	btnCancelMenuLegacy = menu.Text("❌ Cancel")
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

const stationMaxResults = 5

func (c *customContext) sendNearbyStations(loc *tele.Location) error {
	err, cleanup := c.sendStationLoader()
	if err != nil {
		return err
	}
	defer cleanup()

	ss, err := c.gira.GetStations(c)
	if err != nil {
		return err
	}

	ss = slices.DeleteFunc(ss, func(i gira.Station) bool {
		return i.Status != gira.AssetStatusActive
	})

	slices.SortFunc(ss, func(i, j gira.Station) int {
		return cmp.Compare(distance(i, loc), distance(j, loc))
	})

	return c.sendStationList(ss[:min(stationMaxResults, len(ss))], loc)
}

func (c *customContext) sendStationLoader() (error, func()) {
	m, err := c.Bot().Send(c.Recipient(), "Loading stations...")
	if err != nil {
		return err, nil
	}
	err, cleanup := c.sendTyping()
	if err != nil {
		return err, nil
	}
	return nil, func() {
		cleanup()
		if err := c.Bot().Delete(m); err != nil {
			log.Println("error deleting message:", err)
		}
	}
}

func (c *customContext) sendTyping() (error, func()) {
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-time.After(4 * time.Second):
				if err := c.Notify(tele.Typing); err != nil {
					log.Println("error notifying typing:", err)
					return
				}
			case <-done:
				return
			}
		}
	}()
	if err := c.Notify(tele.Typing); err != nil {
		return err, nil
	}
	return nil, func() {
		close(done)
	}
}

// sendStationList sends a list of stations to the user.
// If loc is not nil, it will also show the distance to the station.
// Callers should not pass more than 5 stations at once.
func (c *customContext) sendStationList(stations []gira.Station, loc *tele.Location) error {
	stationsDocks := make([]gira.Docks, len(stations))
	wg := sync.WaitGroup{}
	wg.Add(len(stations))
	for i, s := range stations {
		go func(i int, s gira.StationSerial) {
			defer wg.Done()
			docks, err := c.gira.GetStationDocks(c, s)
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
		freeDocks := min(stationsDocks[i].Free(), s.Docks-s.Bikes)

		btnText := fmt.Sprintf(
			"%s%s: %2d ⚡️ %2d ⚙️ %d 🆓",
			fav,
			s.Number(),
			stationsDocks[i].ElectricBikesAvailable(),
			stationsDocks[i].ConventionalBikesAvailable(),
			freeDocks,
		)

		rm.InlineKeyboard = append(rm.InlineKeyboard, []tele.InlineButton{
			{
				Unique: btnKeyTypeStation,
				Text:   btnText,
				Data:   string(s.Serial),
			},
		})
	}

	rm.InlineKeyboard = append(rm.InlineKeyboard, []tele.InlineButton{{
		Unique: btnKeyTypeCloseMenu,
		Text:   "Close",
	}})

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

func (c *customContext) handleLoggedInText() error {
	txt := c.Text()

	// if got number, first try to treat it as station number:
	if _, err := strconv.Atoi(txt); err == nil {
		stations, err := c.gira.GetStations(c)
		if err != nil {
			return err
		}

		var station gira.Station
		for _, s := range stations {
			if s.Number() == txt {
				station = s
				break
			}
		}

		if station.Status == "" {
			return c.Send("Station not found")
		}

		if station.Status != gira.AssetStatusActive {
			return c.Send("Sorry, station is not active")
		}

		return c.handleStationInner(station.Serial)
	}

	chr := strings.ToLower(txt[:1])[0]
	if chr == 'e' || chr == 'c' {
		// TODO: process as bike number
		// We can't directly get bike by name, so we need to get all stations and then all docks.
		// Maybe we can regularly cache all docks and bikes in the background.
	}

	return c.Send("Unknown command, try /help")
}

func (c *customContext) handleStation() error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	serialStr, cb2, _ := strings.Cut(cb.Data, "|")
	serial := gira.StationSerial(serialStr)

	if cb2 == "delete_msg" {
		// refresh stations cache
		_, err := c.gira.GetStations(c)
		if err != nil {
			return err
		}

		station, err := c.gira.GetStationCached(c, serial)
		if err != nil {
			return err
		}

		if station.Status != gira.AssetStatusActive {
			if err := c.Send("Sorry, station is not active anymore"); err != nil {
				return err
			}

			return c.deleteCallbackMessage()
		}
	}

	if err := c.handleStationInner(serial); err != nil {
		return err
	}

	if cb2 == "delete_msg" {
		return c.deleteCallbackMessage()
	}

	return nil
}

func (c *customContext) handleStationInner(serial gira.StationSerial) error {
	err, cleanup := c.sendTyping()
	if err != nil {
		return err
	}
	defer cleanup()

	// we can call cached version, because we retrieved fresh station list prior while listing stations
	station, err := c.gira.GetStationCached(c, serial)
	if err != nil {
		return err
	}

	// but docks are always retrieved fresh
	docks, err := c.gira.GetStationDocks(c, serial)
	if err != nil {
		return err
	}

	freeDocks := docks.Free()

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

	var maxEBike gira.Bike
	for _, dock := range docks {
		if dock.Bike.Type == gira.BikeTypeElectric && dock.Bike.Number() > maxEBike.Number() {
			maxEBike = *dock.Bike
		}
	}

	var dockBtns []tele.Btn
	for _, dock := range docks {
		dockBtns = append(dockBtns, tele.Btn{
			Unique: btnKeyTypeBike,
			Text:   dock.ButtonString(dock.Bike.Serial == maxEBike.Serial),
			Data:   dock.Bike.CallbackData(),
		})
	}

	rm := &tele.ReplyMarkup{}

	if len(dockBtns) > 1 && len(dockBtns)%2 == 1 {
		dockBtns = append(dockBtns, tele.Btn{
			Text:   " ",
			Unique: btnKeyTypeIgnore,
		})
	}

	btns := rm.Split(2, dockBtns)
	btns = append([]tele.Row{c.getStationFavButtons(station.Serial)}, btns...)
	btns = append(btns, tele.Row{
		{
			Text:   "🔄 Refresh",
			Unique: btnKeyTypeStation,
			Data:   string(serial) + "|delete_msg",
		},
		{
			Text:   fmt.Sprintf("🆓 %d docks", freeDocks),
			Unique: btnKeyTypeIgnore,
		},
		{
			Text:   "❎ Close",
			Unique: btnKeyTypeCloseMenu,
		},
	})
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

	bike, err := gira.BikeFromCallbackData(cb.Data)
	if err != nil {
		return err
	}

	btnsRow := []tele.InlineButton{
		{
			Text:   "🔓 Unlock",
			Unique: btnKeyTypeBikeUnlock,
			Data:   bike.CallbackData(),
		},
		{
			Text:   "❌ Cancel",
			Unique: btnKeyTypeCloseMenu,
		},
	}

	return c.Send(bike.TextString()+"\n\nTapping 'Unlock' will start the trip.", &tele.ReplyMarkup{
		InlineKeyboard: [][]tele.InlineButton{btnsRow},
	})
}

func (c *customContext) handleUnlockBike() error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	err, cleanup := c.sendTyping()
	if err != nil {
		return err
	}
	defer cleanup()

	bike, err := gira.BikeFromCallbackData(cb.Data)
	if err != nil {
		return err
	}

	bikeDesc := bike.TextString() + "\n\n"

	if err := c.Edit(bikeDesc + "Unlocking bike..."); err != nil {
		return err
	}

	ok, err := c.gira.ReserveBike(c, bike.Serial)

	if errors.Is(err, gira.ErrBikeAlreadyReserved) {
		log.Printf("[uid:%d] bike already reserved, trying to cancel: %+v", c.user.ID, bike)
		// at least try to cancel the reservation, ignore errors
		if cancelled, _ := c.gira.CancelBikeReserve(c); cancelled {
			// then, retry to reserve again
			ok, err = c.gira.ReserveBike(c, bike.Serial)
		}
	}

	if err != nil {
		return err
	}

	if !ok {
		log.Printf("[uid:%d] bike reserve failed: %+v", c.user.ID, bike)
		return c.Edit("Bike can't be reserved, try again?")
	}

	ok, err = c.gira.StartTrip(c)
	if err != nil {
		return err
	}

	if !ok {
		log.Printf("[uid:%d] bike start trip failed: %+v", c.user.ID, bike)
		return c.Edit("Bike can't be unlocked, try again?")
	}

	go func() {
		if err := c.watchActiveTrip(true); err != nil {
			c.Bot().OnError(fmt.Errorf("watching active trip: %v", err), c)
		}
	}()

	c.user.CurrentTripMessageID = strconv.Itoa(c.Message().ID)
	return c.Edit(
		bikeDesc+
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

func (c *customContext) respond() error {
	return c.Respond()
}

func (c *customContext) watchActiveTrip(isNewTrip bool) error {
	log.Printf("[uid:%d] watching active trip", c.user.ID)
	// not using c.Send/Edit/etc here and in callees as it might be called upon start while reloading active trips

	c.s.mu.Lock()
	if oldCancel, ok := c.s.activeTripsCancels[c.user.ID]; ok {
		// if for some reason we are already watching active trip, cancel it
		oldCancel()
	}

	// probably no one should have trips longer than a day
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()

	c.s.activeTripsCancels[c.user.ID] = cancel
	c.s.mu.Unlock()

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
		return c.updateEndedTripMessage(trip)
	}

	var costStr string
	if trip.Cost != 0 {
		costStr = fmt.Sprintf("🤑 Cost:  %.0f€\n", trip.Cost)
	}

	_, err := c.Bot().Edit(
		c.getActiveTripMsg(),
		fmt.Sprintf(
			"*Active trip*:\n"+
				"🚲 Bike %s\n"+
				"🕑 Duration ≥%s\n"+
				"%s"+
				"\n🛟 To get Gira support, call +351 211 163 125.",
			trip.Bike,
			trip.PrettyDuration(),
			costStr,
		),
		tele.ModeMarkdown,
	)
	if errors.Is(err, tele.ErrSameMessageContent) {
		// if we got two updates at the same time, we might get this error from TG
		return nil
	}
	return err
}

func (c *customContext) updateEndedTripMessage(trip gira.TripUpdate) error {
	var btns tele.Row
	var costStr string

	if trip.Cost > 0 {
		log.Printf("last trip was not free: %+v", trip)

		costStr = fmt.Sprintf("\n🤑 Cost: %.0f€\n", trip.Cost)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		status, err := c.gira.GetClientInfo(ctx)
		if err != nil {
			log.Printf("[uid:%d] ignored client info error: %v", c.user.ID, err)
		}

		if trip.CanUsePoints {
			btns = append(btns, tele.Btn{
				Unique: btnKeyTypePayPoints,
				Text:   "💰 Pay with points",
				Data:   string(trip.Code),
			})

			if err == nil {
				costStr += fmt.Sprintf("💰 Points balance: %d€\n", status.Bonus/500)
			}
		}

		if trip.CanPayWithMoney {
			btns = append(btns, tele.Btn{
				Unique: btnKeyTypePayMoney,
				Text:   "💶 Pay with money",
				Data:   string(trip.Code),
			})

			if err == nil {
				costStr += fmt.Sprintf("💶 Account balance: %.0f€\n", status.Balance)
			}
		}

		if !trip.CanUsePoints && !trip.CanPayWithMoney {
			costStr += "\n⚠️ You can't pay for this trip with points or money, please use official app to top up and pay for it.\n" +
				"Rating the trip now might trigger some Gira bug and make it free, try not to do that. Or do, I don't care, it's your account."
		} else {
			costStr += "\n🧾 Use buttons below to pay for the trip."
		}
	}

	rm := &tele.ReplyMarkup{}
	rm.Inline(btns)

	if _, err := c.Bot().Send(
		tele.ChatID(c.user.ID),
		fmt.Sprintf(
			"Trip ended, thanks for using BetterGiraBot!\n"+
				"🚲 Bike: %s\n"+
				"🕑 Duration: %s\n"+
				"💰 Points earned: +%d, total %d (%d€)\n"+
				"%s",
			trip.Bike,
			trip.PrettyDuration(),
			trip.TripPoints,
			trip.ClientPoints,
			trip.ClientPoints/500,
			costStr,
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

func (c *customContext) handlePayPoints() error {
	if c.Callback() == nil {
		return c.Send("No callback")
	}

	tc := gira.TripCode(c.Callback().Data)
	if tc == "" {
		return c.Send("No trip code")
	}

	paid, err := c.gira.PayTripWithPoints(c, tc)
	if err != nil {
		return err
	}

	log.Printf("paid for %s with points: %d", tc, paid)

	// remove pay buttons from trip message
	if err := c.Edit(&tele.ReplyMarkup{}); err != nil {
		return err
	}

	return c.Reply(fmt.Sprintf("Paid with points: -%v", paid))
}

func (c *customContext) handlePayMoney() error {
	if c.Callback() == nil {
		return c.Send("No callback")
	}

	tc := gira.TripCode(c.Callback().Data)
	if tc == "" {
		return c.Send("No trip code")
	}

	paid, err := c.gira.PayTripWithMoney(c, tc)
	if err != nil {
		return err
	}

	log.Printf("paid for %s with money: %d", tc, paid)

	// remove pay buttons from trip message
	if err := c.Edit(&tele.ReplyMarkup{}); err != nil {
		return err
	}

	return c.Reply(fmt.Sprintf("Paid with money: -%v", paid))
}

func (c *customContext) handleSendRateMsg() error {
	// not using c.Send/Edit/etc as it might be called upon start while reloading active trips
	log.Printf("[uid:%d] sending rate message", c.user.ID)

	if c.user.CurrentTripCode == "" {
		return fmt.Errorf("no saved trip code, can't rate")
	}

	c.user.CurrentTripRating = gira.TripRating{}
	c.user.CurrentTripRateAwaiting = true

	m, err := c.Bot().Send(
		tele.ChatID(c.user.ID),
		messageRateTrip,
		getStarButtons(0),
	)
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
	rm := &tele.ReplyMarkup{}
	rm.Inline(tele.Row{{
		Unique: btnKeyTypeRateCommentCancel,
		Text:   "❌ Cancel",
	}})
	return c.Edit(
		"Please send your comment regarding the trip",
		rm,
	)
}

func (c *customContext) handleCancelAddComment() error {
	c.user.State = UserStateLoggedIn

	return c.Edit(
		messageRateTrip,
		getStarButtons(c.user.CurrentTripRating.Rating),
	)
}

func (c *customContext) handleRateSubmit() error {
	if c.user.CurrentTripCode == "" {
		return c.Edit("No last trip code, can't submit rating")
	}
	if c.user.CurrentTripRating.Rating == 0 {
		return c.Edit("Please select some stars first", getStarButtons(0))
	}

	err, cleanup := c.sendTyping()
	if err != nil {
		return err
	}
	defer cleanup()

	ok, err := c.gira.RateTrip(c, c.user.CurrentTripCode, c.user.CurrentTripRating)
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

	// send separate message to clear annoying typing status
	if err := c.Send(fmt.Sprint("Rating submitted, thanks!\n", stars, comment)); err != nil {
		return err
	}

	if err := c.Delete(); err != nil {
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

const stationMaxFaves = 10

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

func (c *customContext) handleShowMapLegacy() error {
	return c.Send("This map button is no longer used. Yay, shorter menu!", menu)
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
		s, err := c.gira.GetStationCached(c, serial)
		if err != nil {
			return err
		}
		stations = append(stations, s)
	}

	stations = slices.DeleteFunc(stations, func(i gira.Station) bool {
		return i.Status != gira.AssetStatusActive
	})

	slices.SortFunc(stations, func(i, j gira.Station) int {
		// first. compare by their label
		if c := cmp.Compare(c.user.Favorites[i.Serial], c.user.Favorites[j.Serial]); c != 0 {
			return c
		}
		// then, just by number
		return cmp.Compare(i.Number(), j.Number())
	})

	return c.sendStationList(stations, nil)
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

var debugStatsFirebaseToken = ""

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

	getAccessToken := func() (string, error) {
		ts := c.getTokenSource()
		tok, err := ts.Token()
		if err != nil {
			return "", err
		}
		return tok.AccessToken, nil
	}

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
		"token": func() (any, error) {
			return getAccessToken()
		},
		"fbToken": func() (any, error) {
			tok, err := getAccessToken()
			if err != nil {
				return nil, err
			}
			return firebasetoken.FetchRaw(c, tok)
		},
		"fbTokenEnc": func() (any, error) {
			tok, err := getAccessToken()
			if err != nil {
				return nil, err
			}
			return firebasetoken.Get(c, tok)
		},
		"fbStats": func() (any, error) {
			// nah, race conditions shouldn't happen here
			if debugStatsFirebaseToken == "" {
				tok, err := getAccessToken()
				if err != nil {
					return nil, err
				}
				fbt, err := firebasetoken.FetchRaw(c, tok)
				if err != nil {
					return nil, err
				}
				debugStatsFirebaseToken = fbt
			}

			return firebasetoken.GetStats(c, debugStatsFirebaseToken)
		},
		"client": func() (any, error) {
			return c.gira.GetClientInfo(c)
		},
		"stations": func() (any, error) {
			return c.gira.GetStations(c)
		},
		"station": func() (any, error) {
			if len(args) == 1 {
				return "missing station serial", nil
			}
			return c.gira.GetStationDocks(c, gira.StationSerial(args[1]))
		},
		"stationByNumber": func() (any, error) {
			if len(args) == 1 {
				return "missing station number", nil
			}
			ss, err := c.gira.GetStations(c)
			if err != nil {
				return nil, err
			}
			for _, s := range ss {
				if s.Number() == args[1] {
					docks, err := c.gira.GetStationDocks(c, s.Serial)
					return map[string]any{
						"station": s,
						"docks":   docks,
					}, err
				}
			}
			return c.gira.GetStationDocks(c, gira.StationSerial(args[1]))
		},
		"activeTrip": func() (any, error) {
			return c.gira.GetActiveTrip(c)
		},
		"trip": func() (any, error) {
			if len(args) == 1 {
				return "missing trip code", nil
			}
			return c.gira.GetTrip(c, gira.TripCode(args[1]))
		},
		"tripHistory": func() (any, error) {
			if len(args) < 3 {
				return "missing page and pageSize", nil
			}
			page, _ := strconv.Atoi(args[1])
			pageSize, _ := strconv.Atoi(args[2])
			return c.gira.GetTripHistory(c, page, pageSize)
		},
		"unratedTrips": func() (any, error) {
			if len(args) < 3 {
				return "missing page and pageSize", nil
			}
			page, _ := strconv.Atoi(args[1])
			pageSize, _ := strconv.Atoi(args[2])
			return c.gira.GetUnratedTrips(c, page, pageSize)
		},
		"doReserve": func() (any, error) {
			if len(args) == 1 {
				return "missing bike serial", nil
			}
			return c.gira.ReserveBike(c, gira.BikeSerial(args[1]))
		},
		"doCancel": func() (any, error) {
			return c.gira.CancelBikeReserve(c)
		},
		"doStart": func() (any, error) {
			return c.gira.StartTrip(c)
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
			return c.gira.RateTrip(c, gira.TripCode(args[1]), req)
		},
		"doPayPoints": func() (any, error) {
			if len(args) == 1 {
				return "missing trip code", nil
			}
			return c.gira.PayTripWithPoints(c, gira.TripCode(args[1]))
		},
		"doPayMoney": func() (any, error) {
			if len(args) == 1 {
				return "missing trip code", nil
			}
			return c.gira.PayTripWithMoney(c, gira.TripCode(args[1]))
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
		"broadcast": func() (any, error) {
			args := strings.SplitN(text, " ", 3)
			if len(args) < 3 {
				return "usage: broadcast id1,id2,id3 message (may be multiline)", nil
			}
			ids := strings.Split(args[1], ",")
			msg := args[2]
			var errs []error
			for _, idStr := range ids {
				id, _ := strconv.Atoi(idStr)
				if _, err := c.Bot().Send(tele.ChatID(id), msg, tele.NoPreview, tele.ModeMarkdown); err != nil {
					errs = append(errs, fmt.Errorf("id %d: %w", id, err))
				}
				time.Sleep(100 * time.Millisecond)
			}
			if len(errs) > 0 {
				return "", fmt.Errorf("failed sending to some users: %v", errs)
			}
			return "ok", nil
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

	var valStr []byte
	switch v := val.(type) {
	case string:
		valStr = []byte(v)
	case []byte:
		valStr = v
	default:
		valStr, err = json.MarshalIndent(val, "", "  ")
		if err != nil {
			return err
		}
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
