package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/oauth2"
	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"girabot/internal/gira"
	"girabot/internal/giraauth"
)

type User struct {
	// ID is a telegram user ID
	ID int64 `gorm:"primarykey"`

	TGName     string
	TGUsername string

	// State is a state of user
	State UserState

	Email          string
	EmailMessageID int

	Favorites         map[gira.StationSerial]string `gorm:"serializer:json"`
	EditingStationFav gira.StationSerial

	CurrentTripMessageID string
	CurrentTripCode      gira.TripCode
	CurrentTripRating    gira.TripRating `gorm:"serializer:json"`

	// either stations sorted by distance or favorites sorted by name
	LastSearchResults []gira.StationSerial `gorm:"serializer:json"`
	// if nil, will not show distances
	LastSearchLocation *tele.Location `gorm:"serializer:json"`

	SentDonateMessage bool
}

type filteredUser User

func (u filteredUser) String() string {
	if u.LastSearchLocation != nil {
		u.LastSearchLocation = &tele.Location{Lat: 1, Lng: 1}
	}
	// print only number of results
	u.LastSearchResults = []gira.StationSerial{
		gira.StationSerial(fmt.Sprint(len(u.LastSearchResults))),
	}
	u.Favorites = map[gira.StationSerial]string{
		gira.StationSerial(fmt.Sprint(len(u.Favorites))): "",
	}
	return fmt.Sprintf("%+v", User(u))
}

type Token struct {
	ID    int64         `gorm:"primarykey"`
	Token *oauth2.Token `gorm:"serializer:json"`
}

type server struct {
	db   *gorm.DB
	bot  *tele.Bot
	auth *giraauth.Client

	mu sync.Mutex
	// tokenSources is a map of user ID to token source.
	// It's used to cache token sources, also to persist one instance of token source per user due to locking.
	tokenSouces map[int64]*tokenSource
}

func main() {
	s := server{
		auth:        giraauth.New(http.DefaultClient),
		tokenSouces: map[int64]*tokenSource{},
	}

	// open DB
	db, err := gorm.Open(sqlite.Open("girabot.db"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}
	if err := db.AutoMigrate(&User{}, &Token{}); err != nil {
		log.Fatal(err)
	}

	s.db = db

	// create bot
	b, err := tele.NewBot(tele.Settings{
		Token:   os.Getenv("TOKEN"),
		Poller:  &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: s.onError,
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
	b.Use(middleware.Recover())
	b.Use(allowlist(111504781, 316446182))
	b.Use(s.addCustomContext)

	b.Handle("/start", wrapHandler(s.handleStart))
	b.Handle("/login", wrapHandler(s.handleLogin))
	b.Handle(tele.OnText, wrapHandler(s.handleText))

	authed := b.Group()
	authed.Use(s.checkLoggedIn)

	authed.Handle("/help", wrapHandler(s.handleHelp))
	authed.Handle("/status", wrapHandler(s.handleStatus))
	authed.Handle(tele.OnLocation, wrapHandler(s.handleLocation))

	// some debug endpoints
	authed.Handle("/test", wrapHandler(s.handleLocationTest), allowlist(111504781))
	authed.Handle("/debug", wrapHandler(s.handleDebug), allowlist(111504781))
	authed.Handle("/rate", wrapHandler(s.handleRate), allowlist(111504781))

	authed.Handle(&btnFavorites, wrapHandler(s.handleShowFavorites))
	authed.Handle(&btnStatus, wrapHandler(s.handleStatus))
	authed.Handle(&btnHelp, wrapHandler(s.handleHelp))
	authed.Handle(&btnFeedback, wrapHandler(s.handleFeedback))

	authed.Handle("\f"+btnKeyTypeStation, wrapHandler(s.handleStation))
	authed.Handle("\f"+btnKeyTypeStationNextPage, wrapHandler(s.handleStationNextPage))
	authed.Handle("\f"+btnKeyTypeBike, wrapHandler(s.handleTapBike))
	authed.Handle("\f"+btnKeyTypeBikeUnlock, wrapHandler(s.handleUnlockBike))
	authed.Handle("\f"+btnKeyTypeCloseMenu, wrapHandler(s.deleteCallbackMessage))

	authed.Handle("\f"+btnKeyTypeAddFav, wrapHandler(s.handleAddFavorite))
	authed.Handle("\f"+btnKeyTypeRemoveFav, wrapHandler(s.handleRemoveFavorite))
	authed.Handle("\f"+btnKeyTypeRenameFav, wrapHandler(s.handleRenameFavorite))

	authed.Handle("\f"+btnKeyTypeRateStar, wrapHandler(s.handleRateStar))
	authed.Handle("\f"+btnKeyTypeRateAddText, wrapHandler(s.handleRateAddText))
	authed.Handle("\f"+btnKeyTypeRateSubmit, wrapHandler(s.handleRateSubmit))

	go s.refreshTokensWatcher()

	log.Println("bot start")
	b.Start()
}

type customContext struct {
	tele.Context

	ctx context.Context

	user *User
	gira *gira.Client
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
			u.TGUsername = c.Sender().Username
			u.TGName = c.Sender().FirstName + " " + c.Sender().LastName
			u.Favorites = make(map[gira.StationSerial]string)

			res = s.db.Create(&u)
			if res.Error != nil {
				return res.Error
			}
		}

		log.Printf("bot call, action: '%s', user: %+v", getAction(c), filteredUser(u))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		girac := gira.New(oauth2.NewClient(ctx, s.getTokenSource(u.ID)))

		defer func() {
			if err := s.db.Save(&u).Error; err != nil {
				log.Println("error saving user:", err)
			}
		}()

		return next(&customContext{
			Context: c,
			ctx:     ctx,
			gira:    girac,
			user:    &u,
		})
	}
}

func (s *server) onError(err error, c tele.Context) {
	msg := fmt.Sprintf("recovered error from @%v (%v): %+v", c.Sender().Username, getAction(c), err)
	log.Println("bot:", msg)

	if _, err := s.bot.Send(tele.ChatID(111504781), msg); err != nil {
		log.Println("bot: error sending recovered error:", err)
	}

	if c.Chat() != nil && c.Chat().ID != 111504781 {
		msg := fmt.Sprintf(
			"Internal error: %v.\nBot developer has been notified.",
			err,
		)
		if err := c.Send(msg); err != nil {
			log.Println("bot: error sending recovered error to user:", err)
		}
	}
}

func getAction(c tele.Context) string {
	if c.Callback() != nil {
		return fmt.Sprintf("cb: uniq:%s, data:%s", c.Callback().Unique, c.Callback().Data)
	}
	if c.Message().Location != nil {
		return "<locatiion>"
	}
	return c.Text()
}

func (s *server) refreshTokensWatcher() {
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	for {
		select {
		case <-time.After(time.Hour + time.Duration(rand.Intn(300))*time.Second):
			log.Println("refreshing tokens")
			var tokens []Token
			if err := s.db.Find(&tokens).Error; err != nil {
				s.bot.OnError(fmt.Errorf("error getting tokens for refresh: %v", err), nil)
				continue
			}

			for _, tok := range tokens {
				// Access key expiry is 2 minutes, refresh key expiry is at least 7 days (?)
				// TODO: fill correct duration
				if time.Since(tok.Token.Expiry) < 6*24*time.Hour {
					continue
				}

				log.Println("refreshing token for", tok.ID)
				_, err := s.getTokenSource(tok.ID).Token()
				if err != nil {
					s.bot.OnError(fmt.Errorf("refreshing token for %d: %v", tok.ID, err), nil)
					continue
				}
			}
		case <-done:
			return
		}
	}
}

// getTokenSource returns token source for user. It returns cached token source if it exists.
func (s *server) getTokenSource(uid int64) *tokenSource {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ts, ok := s.tokenSouces[uid]; ok {
		return ts
	}

	s.tokenSouces[uid] = &tokenSource{
		db:   s.db,
		auth: s.auth,
		uid:  uid,
	}
	return s.tokenSouces[uid]
}

// tokenSource is an oauth2 token source that saves token to database.
// It also refreshes token if it's invalid. It's safe for concurrent use.
type tokenSource struct {
	db   *gorm.DB
	auth *giraauth.Client
	uid  int64

	mu sync.Mutex
}

func (t *tokenSource) Token() (*oauth2.Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var tok Token
	if err := t.db.First(&tok, t.uid).Error; err != nil {
		return nil, err
	}

	l := log.New(os.Stderr, fmt.Sprintf("tokenSource[uid:%d] ", t.uid), log.LstdFlags)

	if tok.Token.Valid() {
		l.Printf("token is valid")
		return tok.Token, nil
	}

	l.Printf("token is invalid, refreshing")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	newToken, err := t.auth.Refresh(ctx, tok.Token.RefreshToken)
	if err != nil {
		l.Printf("refresh error: %v", err)
		return nil, err
	}
	l.Printf("refreshed ok")

	tok.Token = newToken
	if err := t.db.Save(&tok).Error; err != nil {
		l.Printf("save error: %v", err)
		return nil, err
	}

	return newToken, nil
}

// wrapHandler wraps handler that accepts custom context to handler that accepts telebot context.
func wrapHandler(f func(cc *customContext) error) func(tele.Context) error {
	return func(c tele.Context) error {
		return f(c.(*customContext))
	}
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
