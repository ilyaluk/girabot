package main

import (
	"cmp"
	"context"
	"encoding/base64"
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
	"unicode"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"
	"gorm.io/gorm/clause"

	"github.com/ilyaluk/girabot/internal/gira"
	"github.com/ilyaluk/girabot/internal/vaimoo"
)

func setupHandlers(s *server) {
	s.bot.Use(middleware.Recover())
	s.bot.Use(s.checkUpdateIDMiddleware)
	s.bot.Use(s.addCustomContext)

	s.bot.Handle("/start", wrapHandler((*customContext).handleStart))
	s.bot.Handle("/login", wrapHandler((*customContext).handleLogin))
	s.bot.Handle(cmdLoginLink, wrapHandler((*customContext).handleLoginLink))
	s.bot.Handle(tele.OnText, wrapHandler((*customContext).handleText))

	s.bot.Handle("/debug", wrapHandler((*customContext).handleDebug), allowlist(*adminID))
	s.bot.Handle("\f"+btnKeyTypeRetryDebug, wrapHandler((*customContext).handleDebugRetry), allowlist(*adminID))

	authed := s.bot.Group()
	authed.Use(s.checkLoggedIn)

	authed.Handle("/help", wrapHandler((*customContext).handleHelp))
	authed.Handle("/status", wrapHandler((*customContext).handleStatus))
	authed.Handle(tele.OnLocation, wrapHandler((*customContext).handleLocation))
	authed.Handle("/rate", wrapHandler((*customContext).handleSendRateMsg))

	authed.Handle("/test", wrapHandler((*customContext).handleLocationTest), allowlist(*adminID))

	authed.Handle(&btnFavorites, wrapHandler((*customContext).handleShowFavorites))
	authed.Handle(&btnStatus, wrapHandler((*customContext).handleStatus))
	authed.Handle(&btnHelp, wrapHandler((*customContext).handleHelp))

	authed.Handle(&btnLegacyMap, wrapHandler((*customContext).handleShowMapLegacy))
	authed.Handle(&btnLegacyCancelMenu, wrapHandler((*customContext).handleShowMapLegacy))
	authed.Handle(&btnLegacyFeedback, wrapHandler((*customContext).handleFeedback))

	authed.Handle("\f"+btnKeyTypeStation, wrapHandler((*customContext).handleStation))
	authed.Handle("\f"+btnKeyTypeBike, wrapHandler((*customContext).handleTapBike))
	authed.Handle("\f"+btnKeyTypeBikeUnlock, wrapHandler((*customContext).handleUnlockBike))
	authed.Handle("\f"+btnKeyTypeCloseMenu, wrapHandler((*customContext).deleteCallbackMessageWithReply))
	authed.Handle("\f"+btnKeyTypeCloseMenuKeepReply, wrapHandler((*customContext).deleteCallbackMessage))
	authed.Handle("\f"+btnKeyTypeIgnore, wrapHandler((*customContext).respond))

	authed.Handle("\f"+btnKeyTypeAddFav, wrapHandler((*customContext).handleAddFavorite))
	authed.Handle("\f"+btnKeyTypeRemoveFav, wrapHandler((*customContext).handleRemoveFavorite))
	authed.Handle("\f"+btnKeyTypeRenameFav, wrapHandler((*customContext).handleRenameFavorite))

	authed.Handle("\f"+btnKeyTypeRateStar, wrapHandler((*customContext).handleRateStar))
	authed.Handle("\f"+btnKeyTypeRateAddText, wrapHandler((*customContext).handleRateAddText))
	authed.Handle("\f"+btnKeyTypeRateCommentCancel, wrapHandler((*customContext).handleCancelAddComment))
	authed.Handle("\f"+btnKeyTypeRateSubmit, wrapHandler((*customContext).handleRateSubmit))
}

// wrapHandler wraps handler that accepts custom context to handler that accepts telebot context.
func wrapHandler(f func(cc *customContext) error) func(tele.Context) error {
	return func(c tele.Context) error {
		return f(c.(*customContext))
	}
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

	btnKeyTypeRetryDebug = "retry_debug"

	btnKeyTypeIgnore = "ignore"

	// cmdLoginLink is also matched in getAction to keep credentials out of logs
	cmdLoginLink = "/loginlink"
)

var (
	menu = &tele.ReplyMarkup{ResizeKeyboard: true}

	btnLocation  = menu.Location("📍 Location")
	btnFavorites = menu.Text("⭐️ Favorites")
	btnStatus    = menu.Text("ℹ️ Status")
	btnHelp      = menu.Text("❓ Help")

	btnLegacyMap        = menu.Text("🗺️ Map")
	btnLegacyFeedback   = menu.Text("📝 Feedback")
	btnLegacyCancelMenu = menu.Text("❌ Cancel")
)

func init() {
	menu.Reply(
		menu.Row(btnLocation, btnFavorites),
		menu.Row(btnStatus, btnHelp, btnLegacyFeedback),
	)
}

func (c *customContext) handleStart() error {
	// a deep link can carry credentials, see parseLoginPayload for the format
	email, pwd, err := parseLoginPayload(c.Message().Payload)
	if err == nil {
		return c.handleDeepLinkLogin(email, pwd)
	}

	badLink := !errors.Is(err, errNotLoginPayload)
	if badLink {
		log.Println("bot: bad login deep link:", err)
		// The payload might still hold a password.
		c.tryDeleteCredentials()
	}

	if err := c.Send(messageHello, tele.ModeMarkdown); err != nil {
		return err
	}

	if badLink {
		if err := c.Send("⚠️ The login link you used is malformed, let's log in the manual way."); err != nil {
			return err
		}
	}

	return c.handleLogin()
}

const (
	// loginPayloadPrefix marks a payload that carries credentials. Single
	// character, as base64 of "email:password" eats the budget below fast.
	loginPayloadPrefix = "L"
	// maxStartPayloadLen is the deep link payload limit Telegram documents
	maxStartPayloadLen = 64
)

var errNotLoginPayload = errors.New("payload is not a login one")

// makeLoginPayload encodes credentials into a deep link payload.
func makeLoginPayload(email, password string) (string, error) {
	payload := loginPayloadPrefix + base64.RawURLEncoding.EncodeToString([]byte(email+":"+password))
	if len(payload) > maxStartPayloadLen {
		return "", fmt.Errorf("payload of %d characters does not fit into a deep link", len(payload))
	}
	return payload, nil
}

// maxLoginLinkCredsLen is the longest "email:password" that fits into a deep
// link payload.
var maxLoginLinkCredsLen = func() int {
	n := 0
	for len(loginPayloadPrefix)+base64.RawURLEncoding.EncodedLen(n+1) <= maxStartPayloadLen {
		n++
	}
	return n
}()

// parseLoginPayload decodes credentials out of a payload of the form
// "L<base64url(email:password)>". Telegram only passes A-Z, a-z, 0-9, '_' and
// '-' through a deep link, hence base64url.
func parseLoginPayload(payload string) (email, password string, err error) {
	enc, ok := strings.CutPrefix(payload, loginPayloadPrefix)
	if !ok {
		return "", "", errNotLoginPayload
	}

	// Padding is not allowed in a payload, but be lenient to link generators.
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(enc, "="))
	if err != nil {
		return "", "", fmt.Errorf("decoding payload: %w", err)
	}

	email, password, ok = strings.Cut(string(raw), ":")
	if !ok {
		return "", "", errors.New("payload has no email/password separator")
	}
	if password == "" {
		return "", "", errors.New("payload has empty password")
	}

	if !validEmail(email) {
		return "", "", errors.New("payload has invalid email")
	}

	return email, password, nil
}

// validEmail reports whether s is a bare email address, e.g. not one with a
// display name around it.
func validEmail(s string) bool {
	parsed, err := mail.ParseAddress(s)
	return err == nil && parsed.Address == s
}

// splitCredentials splits an "email<whitespace>password" pair, the way one
// would paste both at once. ok is false if s doesn't look like such a pair.
func splitCredentials(s string) (email, password string, ok bool) {
	s = strings.TrimSpace(s)

	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return "", "", false
	}

	email, password = s[:i], strings.TrimSpace(s[i:])
	if password == "" || !validEmail(email) {
		return "", "", false
	}
	return email, password, true
}

// commandArgs returns everything after the command name of a message text.
// Message.Payload can't be used for that, as it stops at the first newline.
func commandArgs(text string) string {
	i := strings.IndexFunc(text, unicode.IsSpace)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(text[i:])
}

// handleLoginLink replies with a deep link that logs whoever opens it in with
// the credentials given as command arguments.
func (c *customContext) handleLoginLink() error {
	email, pwd, ok := splitCredentials(commandArgs(c.Text()))
	if !ok {
		return c.Send(messageLoginLinkUsage, tele.ModeMarkdown)
	}

	c.tryDeleteCredentials()

	payload, err := makeLoginPayload(email, pwd)
	if err != nil {
		log.Println("bot: not making a login link:", err)
		return c.Send(fmt.Sprintf(
			"Email and password are too long for a Telegram deep link: "+
				"they have to fit into %d characters together, yours take %d. 😔",
			// the limit is on "email:password", so the separator is not the user's to spend
			maxLoginLinkCredsLen-1, len(email)+len(pwd),
		))
	}

	// A payload can't hold a plaintext email, so an extra query parameter
	// labels the link for a human: clients ignore the ones they don't know.
	// Not a #fragment, which the docs call ignored, but tdesktop chokes on.
	// No markdown: a payload may contain '_', which it would eat as italics.
	return c.Send(fmt.Sprintf(
		"🔗 Here's a login link for %s:\n\n"+
			"https://t.me/%s?start=%s&email=%s\n\n"+
			"Whoever opens it gets logged in as this account, so treat it like the password itself. "+
			"Delete this message once you've shared or saved the link.",
		email, c.Bot().Me.Username, payload, email,
	), tele.NoPreview)
}

// handleDeepLinkLogin logs the user in with credentials from a /start deep link.
func (c *customContext) handleDeepLinkLogin(email, password string) error {
	// Clients hide the payload (they show a bare "/start"), but it's still there.
	c.tryDeleteCredentials()

	// Someone re-logging in via a link does not need the intro again.
	if c.user.State < UserStateLoggedIn {
		if err := c.Send(messageHello, tele.ModeMarkdown); err != nil {
			return err
		}
	}

	return c.loginWithCredentials(email, password)
}

// loginWithCredentials logs the user in with credentials that arrived in one
// go, and falls back to the manual flow if Gira does not like them.
func (c *customContext) loginWithCredentials(email, password string) error {
	m, err := c.Bot().Send(c.Recipient(), "Logging in...")
	if err != nil {
		return err
	}

	session, err := c.s.auth.Login(c, email, password)
	if errors.Is(err, vaimoo.ErrInvalidEmail) || errors.Is(err, vaimoo.ErrInvalidCredentials) {
		if _, err := c.Bot().Edit(m, "Gira rejected these credentials, let's log in the manual way."); err != nil {
			return err
		}
		return c.handleLogin()
	}
	if err != nil {
		return err
	}

	return c.finishLogin(session, m)
}

func (c *customContext) handleLogin() error {
	if err := c.Send(messageLogin); err != nil {
		return err
	}

	c.user.State = UserStateWaitingForEmail
	return nil
}

// finishLogin stores a fresh session, marks the user as logged in and greets
// them. progress is a "logging in" message, removed once the status is sent.
func (c *customContext) finishLogin(session *vaimoo.Session, progress tele.Editable) error {
	// Only a login that skipped handleLogin, which resets the state, can find
	// the user logged in: a deep link swapping accounts. Spare them the help.
	relogin := c.user.State >= UserStateLoggedIn

	// the ID is dropped below, so this is the last chance to delete the message
	c.tryDeleteEmail()

	dbToken := Token{
		ID:      c.user.ID,
		Session: session,
	}
	if err := c.s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&dbToken).Error; err != nil {
		return err
	}
	c.getSessionSource().set(session)

	if err := c.handleStatus(); err != nil {
		return err
	}

	if err := c.Bot().Delete(progress); err != nil {
		return err
	}

	c.user.Email = ""
	c.user.EmailMessageID = 0
	c.user.State = UserStateLoggedIn

	if relogin {
		return nil
	}
	return c.handleHelp()
}

func (c *customContext) handleText() error {
	switch c.user.State {
	case UserStateNone:
		return c.handleStart()
	case UserStateWaitingForEmail:
		// A user might send both credentials at once, e.g. as two lines.
		if email, pwd, ok := splitCredentials(c.Text()); ok {
			c.tryDeleteCredentials()
			return c.loginWithCredentials(email, pwd)
		}

		email := c.Text()
		if !validEmail(email) {
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

		session, err := c.s.auth.Login(c, c.user.Email, pwd)
		if errors.Is(err, vaimoo.ErrInvalidEmail) {
			if _, err := c.Bot().Edit(m, "Invalid email, please start over."); err != nil {
				return err
			}

			c.tryDeleteEmail()
			c.tryDeleteCredentials()

			return c.handleLogin()
		}

		if errors.Is(err, vaimoo.ErrInvalidCredentials) {
			if _, err := c.Bot().Edit(m,
				"Invalid credentials, please try different password.\n"+
					"To change email, run /login.",
			); err != nil {
				return err
			}

			c.tryDeleteCredentials()
			return nil
		}
		if err != nil {
			return err
		}

		c.tryDeleteCredentials()

		return c.finishLogin(session, m)
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

// tryDeleteCredentials removes the current message, which holds credentials.
// A message too old or already gone is not worth failing a login over.
func (c *customContext) tryDeleteCredentials() {
	if err := c.Delete(); err != nil {
		log.Println("bot: error deleting message with credentials:", err)
	}
}

// tryDeleteEmail removes the remembered message with the user's email, if any.
func (c *customContext) tryDeleteEmail() {
	if c.user.EmailMessageID == 0 {
		return
	}
	if err := c.deleteMessage(c.user.EmailMessageID); err != nil {
		log.Println("bot: error deleting message with email:", err)
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
				s.Name,
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
			"Balance: `%.2f€`%s\n"+
			"%s",
		info.Name,
		info.Balance,
		balanceWarning,
		subscr,
	), tele.ModeMarkdown)
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
	stationsBikes := make([]gira.Bikes, len(stations))
	wg := sync.WaitGroup{}
	wg.Add(len(stations))
	for i, s := range stations {
		go func(i int, s gira.StationSerial) {
			defer wg.Done()
			bikes, err := c.gira.GetStationBikes(c, s)
			if err != nil {
				return
			}
			stationsBikes[i] = bikes
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

		btnText := fmt.Sprintf(
			"%s%s: %2d ⚡️ %2d 🆓",
			fav,
			s.Number(),
			len(stationsBikes[i]),
			s.FreeDocks,
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

	// a bike plate, e.g. E2032, names a bike directly
	if chr := strings.ToLower(txt[:1])[0]; chr == 'e' {
		if _, err := strconv.Atoi(txt[1:]); err == nil {
			return c.handleBikeName(txt)
		}
	}

	return c.Send("Unknown command, try /help")
}

// handleBikeName sends the unlock message for a bike named by its plate.
func (c *customContext) handleBikeName(name string) error {
	err, cleanup := c.sendTyping()
	if err != nil {
		return err
	}
	defer cleanup()

	bike, err := c.gira.GetBike(c, name)
	if errors.Is(err, gira.ErrNoBikeFound) {
		return c.Send("Bike not found")
	}
	if err != nil {
		return err
	}

	return c.sendBikeMessage(bike.CallbackData())
}

func (c *customContext) handleStation() error {
	cb := c.Callback()
	if cb == nil {
		return c.Send("No callback")
	}

	serialStr, cb2, _ := strings.Cut(cb.Data, "|")
	serial := gira.StationSerial(serialStr)

	if cb2 == "delete_msg" {
		station, err := c.gira.GetStation(c, serial)
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

	// Read fresh: the rider is about to act on this keyboard.
	station, err := c.gira.GetStation(c, serial)
	if err != nil {
		return err
	}

	bikes, err := c.gira.GetStationBikes(c, serial)
	if err != nil {
		return err
	}

	// the newest bikes have the highest plate numbers, so mark the top one
	var maxBike gira.Bike
	for _, bike := range bikes {
		if bike.Number() > maxBike.Number() {
			maxBike = bike
		}
	}

	var dockBtns []tele.Btn
	for _, bike := range bikes {
		dockBtns = append(dockBtns, tele.Btn{
			Unique: btnKeyTypeBike,
			Text:   bike.ButtonString(bike.Name == maxBike.Name),
			Data:   bike.CallbackData(),
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
			Text:   fmt.Sprintf("🆓 %d docks", station.FreeDocks),
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

	return c.sendBikeMessage(cb.Data)
}

func (c *customContext) sendBikeMessage(bikeCallback string) error {
	bike, err := gira.BikeFromCallbackData(bikeCallback)
	if err != nil {
		return err
	}

	// save for re-sending bike after trip interval limit
	c.user.LastSelectedBikeCb = bikeCallback

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

	if err := c.gira.StartTrip(c, bike.CommID); err != nil {
		return err
	}

	// The watcher reads both of these, so they are set before it starts.
	c.user.CurrentTripBike = bike.Name
	c.user.CurrentTripMessageID = strconv.Itoa(c.Message().ID)
	err = c.s.db.Model(c.user).Updates(map[string]any{
		"current_trip_bike":       c.user.CurrentTripBike,
		"current_trip_message_id": c.user.CurrentTripMessageID,
	}).Error
	if err != nil {
		return err
	}

	go func() {
		if err := c.watchActiveTrip(true); err != nil {
			c.Bot().OnError(fmt.Errorf("watching active trip: %v", err), c)
		}
	}()

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

	// A watch resumed after a restart has to reconcile a trip that may already
	// be over, which the code from the database is what identifies.
	var resumeCode gira.TripCode
	if !isNewTrip {
		resumeCode = c.user.CurrentTripCode
	}

	ch := c.gira.WatchTrip(ctx, c.user.CurrentTripBike, resumeCode)

	// TODO: check for case with two bikes and fast return

	if isNewTrip {
		// first channel pass -- wait for new trip
		if err := c.waitForTripStart(ch); err != nil {
			return err
		}
	}

	// second channel pass -- look for current trip updates
	for trip := range ch {
		log.Printf("[uid:%d] active trip update: %+v", c.user.ID, trip)

		if trip.ErrorCode != 0 {
			return c.reportTripWatchFailure(trip)
		}

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

// waitForTripStart reads TripUpdates from the channel until the trip the rider
// just unlocked shows up. It then records the trip code and sends the initial
// message.
func (c *customContext) waitForTripStart(ch <-chan gira.TripUpdate) error {
	for trip := range ch {
		log.Printf("[uid:%d] got some current trip: %+v", c.user.ID, trip)

		if trip.ErrorCode != 0 {
			return c.reportTripWatchFailure(trip)
		}

		if trip.Finished || trip.Code == "" {
			// got update for some old trip
			continue
		}

		log.Printf("[uid:%d] active trip started: %+v", c.user.ID, trip)

		c.user.CurrentTripCode = trip.Code
		if trip.Bike != "" {
			c.user.CurrentTripBike = trip.Bike
		}
		err := c.s.db.Model(c.user).Updates(map[string]any{
			"current_trip_code": trip.Code,
			"current_trip_bike": c.user.CurrentTripBike,
		}).Error
		if err != nil {
			return err
		}

		// found trip, update initial message
		return c.updateActiveTripMessage(trip)
	}
	return nil
}

// reportTripWatchFailure tells the rider that the bot lost track of the trip.
func (c *customContext) reportTripWatchFailure(trip gira.TripUpdate) error {
	log.Printf("[uid:%d] trip watch failed: %+v", c.user.ID, trip)

	msg := "I can't see a trip on this bike. If it did unlock, check the official app before trying again."
	switch trip.ErrorCode {
	case gira.TripErrorStartTimeout:
		msg = "The bike reported a problem while unlocking. " +
			"If it did unlock, check the official app before trying again."
	case gira.TripErrorWatchFailed:
		msg = "I lost track of this trip. Check the official app, and /status once you're logged in again."
	}

	if _, err := c.Bot().Edit(c.getActiveTripMsg(), msg, &tele.ReplyMarkup{}); err != nil {
		log.Printf("[uid:%d] could not update the trip message: %v", c.user.ID, err)
		if _, err := c.Bot().Send(tele.ChatID(c.user.ID), msg); err != nil {
			return err
		}
	}

	c.user.CurrentTripMessageID = ""
	c.user.CurrentTripBike = ""
	return c.s.db.Model(c.user).Updates(map[string]any{
		"current_trip_message_id": "",
		"current_trip_bike":       "",
	}).Error
}

func (c *customContext) updateActiveTripMessage(trip gira.TripUpdate) error {
	if trip.ErrorCode != 0 {
		return c.reportTripWatchFailure(trip)
	}

	if trip.Finished {
		return c.updateEndedTripMessage(trip)
	}

	_, err := c.Bot().Edit(
		c.getActiveTripMsg(),
		fmt.Sprintf(
			"*Active trip*:\n"+
				"🚲 Bike %s\n"+
				"🕑 Duration ≥%s\n"+
				"\n🛟 To get Gira support, call +351 211 163 125.",
			trip.Bike,
			trip.PrettyDuration(),
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
	var details string

	if trip.Distance > 0 {
		details += fmt.Sprintf("📏 Distance: %.1f km\n", trip.Distance/1000)
	}

	if trip.Cost > 0 {
		log.Printf("last trip was not free: %+v", trip)
		details += fmt.Sprintf("🤑 Cost: %.2f€, charged to your Gira wallet\n", trip.Cost)
	}

	if _, err := c.Bot().Send(
		tele.ChatID(c.user.ID),
		fmt.Sprintf(
			"Trip ended, thanks for using BetterGiraBot!\n"+
				"🚲 Bike: %s\n"+
				"🕑 Duration: %s\n"+
				"%s",
			trip.Bike,
			trip.PrettyDuration(),
			details,
		),
	); err != nil {
		return err
	}

	if err := c.Bot().Delete(c.getActiveTripMsg()); err != nil {
		return err
	}
	c.user.CurrentTripMessageID = ""

	return nil
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

	err = c.gira.RateTrip(c, c.user.CurrentTripCode, c.user.CurrentTripBike, c.user.CurrentTripRating)
	if err != nil {
		return err
	}

	stars := strings.Repeat("⭐️", c.user.CurrentTripRating.Rating) + strings.Repeat("☆", 5-c.user.CurrentTripRating.Rating)
	var comment string
	if c.user.CurrentTripRating.Comment != "" {
		comment = fmt.Sprintf("\nComment: %s", c.user.CurrentTripRating.Comment)
	}

	c.user.RateMessageID = ""
	c.user.CurrentTripCode = ""
	c.user.CurrentTripBike = ""
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
			// A station Gira has since retired must not take the whole list
			// down with it, or the favorite becomes impossible to remove.
			log.Printf("[uid:%d] skipping favorite station %s: %v", c.user.ID, serial, err)
			continue
		}
		stations = append(stations, s)
	}

	if len(stations) == 0 {
		return c.Send("None of your favorite stations are available right now")
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
		session, err := c.getSessionSource().Session(false)
		if err != nil {
			return "", err
		}
		return session.AccessToken, nil
	}

	handlers := map[string]func() (any, error){
		"user": func() (any, error) {
			return c.user, nil
		},
		"session": func() (any, error) {
			session, err := c.getSessionSource().Session(false)
			if err != nil {
				return nil, err
			}
			redacted := *session
			redacted.AccessToken = "<redacted>"
			redacted.RefreshToken = "<redacted>"
			return redacted, nil
		},
		"token": func() (any, error) {
			return getAccessToken()
		},
		"refresh": func() (any, error) {
			session, err := c.getSessionSource().Session(true)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"expiry":         session.Expiry,
				"refresh_expiry": session.RefreshExpiry,
			}, nil
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
			serial := gira.StationSerial(args[1])
			station, err := c.gira.GetStation(c, serial)
			if err != nil {
				return nil, err
			}
			bikes, err := c.gira.GetStationBikes(c, serial)
			return map[string]any{"station": station, "bikes": bikes}, err
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
					bikes, err := c.gira.GetStationBikes(c, s.Serial)
					return map[string]any{
						"station": s,
						"bikes":   bikes,
					}, err
				}
			}
			return "station not found", nil
		},
		"bike": func() (any, error) {
			if len(args) == 1 {
				return "missing bike name", nil
			}
			return c.gira.GetBike(c, args[1])
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
		"doStart": func() (any, error) {
			if len(args) == 1 {
				return "missing bike communication id", nil
			}
			return nil, c.gira.StartTrip(c, args[1])
		},
		"doRateTrip": func() (any, error) {
			args := strings.SplitN(text, " ", 5)
			if len(args) < 5 {
				return "missing trip code, bike name, rating and comment", nil
			}
			rating, _ := strconv.Atoi(args[3])
			req := gira.TripRating{
				Rating:  rating,
				Comment: args[4],
			}
			return nil, c.gira.RateTrip(c, gira.TripCode(args[1]), args[2], req)
		},
		"watchTrip": func() (any, error) {
			if len(args) == 1 {
				return "missing duration", nil
			}

			dur, err := time.ParseDuration(args[1])
			if err != nil {
				return nil, err
			}

			ctx, cancel := context.WithTimeout(context.Background(), dur)
			defer cancel()

			for trip := range c.gira.WatchTrip(ctx, c.user.CurrentTripBike, "") {
				_ = c.Send(fmt.Sprintf("Current trip: `%+v`", trip), tele.ModeMarkdown)
			}

			return nil, nil
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
