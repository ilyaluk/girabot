package main

import (
	"context"
	crand "crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof" // exposed only at localhost
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ilyaluk/girabot/internal/gira"
	"github.com/ilyaluk/girabot/internal/vaimoo"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type User struct {
	// ID is a telegram user ID
	ID int64 `gorm:"primarykey"`

	CreatedAt time.Time

	TGName     string
	TGUsername string

	// State is a state of user
	State UserState

	Email          string
	EmailMessageID int

	Favorites         map[gira.StationSerial]string `gorm:"serializer:json"`
	EditingStationFav gira.StationSerial

	// VaimooMigrated records that the identifiers saved here are VAIMOO ones
	// rather than the EMEL ones used before the backend migration.
	VaimooMigrated bool

	CurrentTripCode gira.TripCode
	// CurrentTripBike is the plate of the bike the trip runs on. Rating a trip
	// needs it, and the backend does not report it once the trip is over.
	CurrentTripBike         string
	CurrentTripMessageID    string
	RateMessageID           string
	CurrentTripRating       gira.TripRating `gorm:"serializer:json"`
	CurrentTripRateAwaiting bool

	// for sending the bike message again after trip interval limit
	LastSelectedBikeCb string

	FinishedTrips int

	SentDonateMessage bool
}

func (c *customContext) getActiveTripMsg() tele.Editable {
	return tele.StoredMessage{
		ChatID:    c.user.ID,
		MessageID: c.user.CurrentTripMessageID,
	}
}

func (c *customContext) getRateMsg() tele.Editable {
	return tele.StoredMessage{
		ChatID:    c.user.ID,
		MessageID: c.user.RateMessageID,
	}

}

// filteredUser is a User with some fields filtered out for logging.
type filteredUser User

func (u filteredUser) String() string {
	if u.Email != "" {
		u.Email = "<email>"
	}
	u.Favorites = map[gira.StationSerial]string{
		gira.StationSerial(fmt.Sprint(len(u.Favorites))): "",
	}
	return fmt.Sprintf("%+v", User(u))
}

// Token holds one rider's VAIMOO session. The table keeps its name from when
// the backend handed out plain OAuth tokens.
type Token struct {
	ID      int64           `gorm:"primarykey"`
	Session *vaimoo.Session `gorm:"serializer:json"`
}

type server struct {
	db   *gorm.DB
	bot  *tele.Bot
	auth *vaimoo.Client

	mu sync.Mutex
	// sessionSources is a map of user ID to session source.
	// It's used to cache session sources, also to persist one instance of session source per user due to locking.
	sessionSources map[int64]*sessionSource
	// activeTripsCancels is a map of user ID to cancel function for active trip watcher.
	// It's used to cancel active trip watcher if for some reason two watchers are started for one user.
	activeTripsCancels map[int64]context.CancelFunc
	// lastUpdateID is a last update ID to avoid processing the same update twice.
	lastUpdateID int
}

var (
	adminID    = flag.Int64("admin-id", 111504781, "admin user ID")
	dbPath     = flag.String("db-path", "girabot.db", "path to sqlite database")
	domain     = flag.String("domain", "luk.moe", "domain for webapp/webhook")
	urlPrefix  = flag.String("url-prefix", "/girabot_prod", "url prefix for webapp")
	listenPort = flag.String("port", "8001", "port to listen on")
	debugPort  = flag.String("debug-port", "9090", "debug port to listen on (metrics/pprof)")
)

func main() {
	flag.Parse()

	s := server{
		auth:               vaimoo.New(&http.Client{}),
		sessionSources:     map[int64]*sessionSource{},
		activeTripsCancels: map[int64]context.CancelFunc{},
	}

	// open DB
	db, err := gorm.Open(sqlite.Open(*dbPath), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}, &Token{}); err != nil {
		log.Fatal(err)
	}

	// The pre-migration column held OAuth tokens for a backend that no longer
	// exists, so drop it rather than keep stale credentials around.
	if db.Migrator().HasColumn(&Token{}, "token") {
		if err := db.Migrator().DropColumn(&Token{}, "token"); err != nil {
			log.Println("could not drop the legacy token column:", err)
		}
	}

	s.db = db

	webhook := &tele.Webhook{
		SecretToken: getRandomString(32),
		Endpoint: &tele.WebhookEndpoint{
			PublicURL: fmt.Sprintf("https://%s%s/webhook", *domain, *urlPrefix),
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/webhook", webhook)
	mux.HandleFunc("/api/stations", s.handleWebStations)
	mux.HandleFunc("/api/selectStation", s.handleWebSelectStation)
	mux.Handle("/", staticServer)

	handler := http.StripPrefix(*urlPrefix, mux)

	go func() {
		log.Println("listening on", *listenPort)
		if err := http.ListenAndServe(net.JoinHostPort("127.0.0.1", *listenPort), handler); err != nil {
			log.Fatal(err)
		}
	}()

	http.Handle("/metrics", promhttp.Handler())
	go func() {
		log.Println("debug server listening on", *debugPort)
		if err := http.ListenAndServe(net.JoinHostPort("127.0.0.1", *debugPort), http.DefaultServeMux); err != nil {
			log.Fatal(err)
		}
	}()

	tgHTTPC := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 100,
		},
	}

	// create bot
	b, err := tele.NewBot(tele.Settings{
		Token:   os.Getenv("TOKEN"),
		Poller:  webhook,
		OnError: s.onError,
		Client:  tgHTTPC,
	})
	if err != nil {
		log.Fatal(err)
	}

	s.bot = b

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	go func() {
		<-done
		log.Println("stopping bot")
		b.Stop()

		d, _ := db.DB()
		_ = d.Close()
	}()

	// register middlewares and handlers
	setupHandlers(&s)

	s.migrateToVaimoo()

	go s.refreshTokensWatcher()
	s.loadActiveTrips()

	log.Println("bot start")
	b.Start()
}

func getRandomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		return ""
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

type customContext struct {
	tele.Context

	ctx context.Context

	s    *server
	user *User
	gira *gira.Client
}

func (s *server) checkUpdateID(upd tele.Update) (doProcess bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// The update's unique identifier. Update identifiers start from a certain
	// positive number and increase sequentially.
	// <...>
	// If there are no new updates for at least a week, then identifier
	// of the next update will be chosen randomly instead of sequentially.
	//
	// So, ignoring updates that are within the last 10 updates, should work fine.
	if s.lastUpdateID-10 < upd.ID && upd.ID <= s.lastUpdateID {
		return false
	}
	s.lastUpdateID = upd.ID
	return true
}

func (s *server) checkUpdateIDMiddleware(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if !s.checkUpdateID(c.Update()) {
			return nil
		}
		return next(c)
	}
}

// addCustomContext is a middleware that wraps telebot context to custom context,
// which includes gira client and user model.
// It also saves updated user model to database.
func (s *server) addCustomContext(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		var u User
		res := s.db.First(&u, c.Sender().ID)
		if errors.Is(res.Error, gorm.ErrRecordNotFound) {
			log.Printf("user %d not found, creating", c.Sender().ID)

			u.ID = c.Sender().ID
			u.CreatedAt = time.Now()
			u.TGUsername = c.Sender().Username
			u.TGName = c.Sender().FirstName + " " + c.Sender().LastName
			u.Favorites = make(map[gira.StationSerial]string)
			u.VaimooMigrated = true

			res = s.db.Create(&u)
			if res.Error != nil {
				return res.Error
			}
		}

		defer func() {
			log.Println("saving user", filteredUser(u))
			// update user in database with changes from handler
			if err := s.db.Save(&u).Error; err != nil {
				log.Println("error saving user:", err)
			}
		}()

		log.Printf("bot call, action: '%s', user: %+v", getAction(c, u), filteredUser(u))

		ctx, cancel := s.newCustomContext(c, &u)
		defer cancel()
		return next(ctx)
	}
}

func (s *server) newCustomContext(c tele.Context, u *User) (*customContext, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	girac := gira.New(&http.Client{}, s.getSessionSource(u.ID))

	return &customContext{
		Context: c,
		ctx:     ctx,
		s:       s,
		user:    u,
		gira:    girac,
	}, cancel
}

var lisbonTZ *time.Location

func init() {
	loc, err := time.LoadLocation("Europe/Lisbon")
	if err != nil {
		log.Fatal(err)
	}
	lisbonTZ = loc
}

func (s *server) onError(err error, c tele.Context) {
	var u User
	if c != nil && c.Chat() != nil {
		s.db.First(&u, c.Chat().ID)
	}

	username := strings.ReplaceAll(u.TGUsername, "_", "\\_")
	if username == "" {
		username = "?"
		if u.ID != 0 {
			username = fmt.Sprintf("[%v](tg://user?id=%v)", u.ID, u.ID)
		}
	}

	adminMsg := fmt.Sprintf("recovered error from @%v (`%v`): `%+v`", username, getAction(c, u), err)
	log.Println("bot:", adminMsg)

	if u.ID != 0 {
		// handle some known errors
		var prettyErr string

		switch {
		case errors.Is(err, tele.ErrMessageNotModified),
			errors.Is(err, tele.ErrSameMessageContent),
			errors.Is(err, tele.ErrNotFoundToDelete),
			strings.Contains(err.Error(), "message can't be deleted for everyone"):
			log.Println("bot: ignoring telegram message error, this happens sometimes")
			return

		case strings.Contains(err.Error(), "read: connection reset by peer") &&
			strings.Contains(err.Error(), "https://api.telegram.org/"):

			log.Println("bot: ignoring connection reset error")
			if _, err := s.bot.Send(tele.ChatID(*adminID), "connreset: "+err.Error(), tele.ModeMarkdown); err != nil {
				log.Println("bot: error sending recovered error:", err)
			}

			return

		case errors.Is(err, tele.ErrMessageNotModified), errors.Is(err, tele.ErrSameMessageContent):
			log.Println("bot: ignoring message not modified error")
			return

		case errors.Is(err, vaimoo.ErrInternalServer):
			prettyErr = "Gira API returned internal server error. Please try again."

		case errors.Is(err, vaimoo.ErrInvalidRefreshToken), errors.Is(err, gira.ErrNotLoggedIn):
			prettyErr = "Your Gira session has expired. Please re-login via /login."

		case errors.Is(err, gira.ErrAlreadyHasActiveTrip):
			prettyErr = "Gira says that you already have an active trip. This is probably their bug. " +
				"Try unlocking bike again, or call Gira support at +351 211 163 125."

		case errors.Is(err, gira.ErrBikeInRepair):
			prettyErr = "Gira says that the bike is in repair. Try other one."

		case errors.Is(err, gira.ErrNotEnoughBalance):
			prettyErr = "You have negative balance and can't unlock the bike. " +
				"Check your balance via /status and top up in official app if needed."

		case errors.Is(err, gira.ErrTripIntervalLimit):
			prettyErr = "You can't start a new trip so soon after the last one. " +
				"Please wait a bit and try again."

			cc, cancel := s.newCustomContext(c, &u)
			defer cancel()

			trips, err := cc.gira.GetTripHistory(cc, 1, 1)
			if err == nil && len(trips) == 1 {
				t := trips[0].EndDate
				delta := time.Since(t).Truncate(time.Second)
				// check for reasonable time for previously ended trip.
				if delta < time.Hour {
					prettyErr = fmt.Sprintf("You can't start a new trip so soon. Last trip ended %v ago.\n", delta)
					if delta < 5*time.Minute {
						// to avoid weird rounding errors, add 1 second
						wait := 5*time.Minute - delta + time.Second
						prettyErr += fmt.Sprintf("Please wait %v and try again.\n\nI'll notify you once this passes!", wait)
						time.AfterFunc(wait, func() {
							// new context, previous one might be already expired
							cc, cancel := s.newCustomContext(c, &u)
							defer cancel()

							if err := cc.Send("You can start a trip now. Enjoy your ride! 🚲"); err != nil {
								log.Println("bot: error sending trip interval message:", err)
								return
							}

							if err := cc.sendBikeMessage(u.LastSelectedBikeCb); err != nil {
								log.Println("bot: error sending bike message after trip interval:", err)
								return
							}
						})
					} else {
						prettyErr += "Weird, you should be able to start a new trip after 5 minutes. Try again later. 🤷🏼"
					}
				}
			}

		case errors.Is(err, gira.ErrHasNoActiveSubscriptions):
			prettyErr = "You don't have any active subscriptions. " +
				"Please buy a subscription in official app and try again."

		case errors.Is(err, gira.ErrBikeAlreadyInTrip):
			prettyErr = "The bike is already in a trip. Try another one."

		case errors.Is(err, gira.ErrNoBikeFound):
			prettyErr = "Gira can't find this bike. Try another one."

		case errors.Is(err, gira.ErrUnableToStartTrip):
			prettyErr = "Gira could not unlock the bike. Try again, or try another one."

		case errors.Is(err, gira.ErrTripNotFound):
			prettyErr = "Gira can't find this trip any more. 🤷🏼"

		case errors.Is(err, gira.ErrForbidden), errors.Is(err, vaimoo.ErrUnauthorized):
			if _, err := s.bot.Send(tele.ChatID(*adminID), "forbidden: "+adminMsg, tele.ModeMarkdown); err != nil {
				log.Println("bot: error sending recovered error:", err)
			}

			prettyErr = "Gira refused the request. Try /login again, and tell @ilyaluk if that doesn't help."

		case errors.Is(err, gira.ErrServiceUnavailable):
			hr := time.Now().In(lisbonTZ).Hour()
			if hr >= 2 && hr < 6 {
				prettyErr = "Gira is not available at night (2-6 AM)."
			} else {
				prettyErr = "Gira service is unavailable. Try again later."
			}
		}

		if prettyErr != "" {
			if err := c.Send(prettyErr); err != nil {
				msg := fmt.Sprintf("error sending pretty error to user %v: `%v`", username, err)
				log.Println("bot:", msg)
				s.bot.Send(tele.ChatID(*adminID), msg, tele.ModeMarkdown)
			}
			return
		}
	}

	if _, err := s.bot.Send(tele.ChatID(*adminID), adminMsg, tele.ModeMarkdown); err != nil {
		log.Println("bot: error sending recovered error:", err)
	}

	if u.ID != 0 && u.ID != *adminID {
		msg := fmt.Sprintf(
			"Internal error: %v.\nBot developer has been notified.",
			err,
		)
		// sometimes error message contains token, redact it
		msg = strings.ReplaceAll(msg, s.bot.Token, "<token>")
		if err := c.Send(msg); err != nil {
			log.Println("bot: error sending recovered error to user:", err)
		}
	}
}

func getAction(c tele.Context, u User) string {
	// user might be of zero value if it's not in database
	if c == nil {
		return "<nil>"
	}

	if c.Callback() != nil {
		return fmt.Sprintf("cb: uniq:%s, data:%s", c.Callback().Unique, c.Callback().Data)
	}
	if c.Message() == nil {
		return fmt.Sprintf("<weird upd: %+v>", c.Update())
	}
	if c.Message().Location != nil {
		return "<location>"
	}

	// do not send PII
	if u.State == UserStateWaitingForEmail {
		return "<email>"
	}
	if u.State == UserStateWaitingForPassword {
		return "<password>"
	}
	// a /start deep link payload may hold credentials
	if c.Message().Payload != "" && strings.HasPrefix(c.Message().Text, "/start") {
		return "/start <payload>"
	}
	// /loginlink arguments are credentials
	if strings.HasPrefix(c.Text(), cmdLoginLink) && commandArgs(c.Text()) != "" {
		return cmdLoginLink + " <credentials>"
	}
	// a pasted email/password pair can arrive in any state
	if _, _, ok := splitCredentials(c.Text()); ok {
		return "<credentials>"
	}

	return c.Text()
}

// refreshTokensWatcher keeps sessions alive. VAIMOO refresh tokens last five
// days and rotate on every use, so a rider who does not open the bot for a week
// would otherwise have to log in again.
func (s *server) refreshTokensWatcher() {
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	for {
		select {
		case <-time.After(time.Hour + time.Duration(rand.Intn(300))*time.Second):
			log.Println("refreshing sessions")
			var tokens []Token
			if err := s.db.Find(&tokens).Error; err != nil {
				s.bot.OnError(fmt.Errorf("error getting sessions for refresh: %v", err), nil)
				continue
			}

			for _, tok := range tokens {
				if tok.Session == nil || tok.Session.RefreshExpiry.IsZero() {
					continue
				}
				// Refresh a day before the refresh token dies, which leaves
				// room for a few failed attempts.
				if time.Until(tok.Session.RefreshExpiry) > 24*time.Hour {
					continue
				}

				log.Println("refreshing session for", tok.ID)
				_, err := s.getSessionSource(tok.ID).Session(true)
				if err == nil {
					continue
				}

				log.Printf("error refreshing session for %d: %v", tok.ID, err)

				// Only the backend refusing the refresh token means the rider
				// has to log in again. Anything else gets another hour.
				if !errors.Is(err, vaimoo.ErrInvalidRefreshToken) {
					s.bot.OnError(fmt.Errorf("session refresh for %d failed, will retry: %v", tok.ID, err), nil)
					continue
				}

				s.bot.OnError(fmt.Errorf("session for %d was refused and removed: %v", tok.ID, err), nil)
				s.db.Delete(&tok)
				s.db.Model(&User{}).Where("id = ?", tok.ID).Update("state", 0)

				_, err = s.bot.Send(tele.ChatID(tok.ID), "Your session has expired. Please log in again via /login.")
				if err != nil {
					log.Printf("error sending session expired message to %d: %v", tok.ID, err)
				}
			}
		case <-done:
			return
		}
	}
}

func (s *server) loadActiveTrips() {
	log.Println("loading active trips")
	var users []User
	if err := s.db.Find(&users).Error; err != nil {
		log.Fatalf("error getting users for active trip load: %v", err)
	}

	for _, u := range users {
		u := u
		if u.CurrentTripCode != "" && !u.CurrentTripRateAwaiting {
			log.Printf("starting active trip watch for %d", u.ID)
			// empty context update, we are not using any shorthands in watchActiveTrip
			c, cancel := s.newCustomContext(s.bot.NewContext(tele.Update{}), &u)
			go func() {
				defer cancel()
				if err := c.watchActiveTrip(false); err != nil {
					s.bot.OnError(fmt.Errorf("watching active trip: %v", err), c)
				}
			}()
		}
	}
}

// getSessionSource returns the session source for a user, creating it if
// needed. One instance per user is kept so that refreshes stay serialised.
func (s *server) getSessionSource(uid int64) *sessionSource {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ss, ok := s.sessionSources[uid]; ok {
		return ss
	}

	s.sessionSources[uid] = &sessionSource{
		db:   s.db,
		auth: s.auth,
		uid:  uid,
	}
	return s.sessionSources[uid]
}

func (c *customContext) getSessionSource() *sessionSource {
	return c.s.getSessionSource(c.user.ID)
}

// sessionSource hands out a rider's VAIMOO session, refreshing and saving it
// when the access token has expired. It is safe for concurrent use.
type sessionSource struct {
	db   *gorm.DB
	auth *vaimoo.Client
	uid  int64

	mu sync.Mutex
	// cached is the session this process last obtained. Refreshing rotates the
	// refresh token and burns the old one, so a refresh that could not be saved
	// must still be honoured here or the rider is locked out.
	cached *vaimoo.Session
}

// set records a session this process just obtained by logging in.
func (t *sessionSource) set(session *vaimoo.Session) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cached = session
}

func (t *sessionSource) Session(force bool) (*vaimoo.Session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	l := log.New(os.Stderr, fmt.Sprintf("sessionSource[uid:%d] ", t.uid), log.LstdFlags)

	var tok Token
	if err := t.db.First(&tok, t.uid).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		// A logged out rider has no row, and no session to fall back on.
		t.cached = nil
		return nil, gira.ErrNotLoggedIn
	}

	session := tok.Session
	if t.cached != nil && (session == nil || t.cached.Expiry.After(session.Expiry)) {
		session = t.cached
	}
	if session == nil {
		return nil, gira.ErrNotLoggedIn
	}

	if !force && session.Valid() {
		l.Printf("session is valid")
		return session, nil
	}

	if !session.RefreshValid() {
		l.Printf("refresh token has expired")
		return nil, vaimoo.ErrInvalidRefreshToken
	}

	l.Printf("session needs a refresh")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	newSession, err := t.auth.Refresh(ctx, session.RefreshToken)
	if err != nil {
		l.Printf("refresh error: %v", err)
		return nil, err
	}
	l.Printf("refreshed ok")

	t.cached = newSession
	tok.Session = newSession
	if err := t.db.Save(&tok).Error; err != nil {
		l.Printf("save error, session lives on in memory only: %v", err)
	}

	return newSession, nil
}

func allowlist(chats ...int64) tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return middleware.Restrict(middleware.RestrictConfig{
			Chats: chats,
			In:    next,
			Out: func(c tele.Context) error {
				log.Printf("bot: user not in allowlist: %+v", c.Sender())
				return nil
			},
		})(next)
	}
}

func (c *customContext) Deadline() (deadline time.Time, ok bool) {
	return c.ctx.Deadline()
}

func (c *customContext) Done() <-chan struct{} {
	return c.ctx.Done()
}

func (c *customContext) Err() error {
	return c.ctx.Err()
}

func (c *customContext) Value(key any) any {
	return c.ctx.Value(key)
}
