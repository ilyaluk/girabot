package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ilyaluk/girabot/internal/firebasetoken"
	"github.com/ilyaluk/girabot/internal/giraauth"
	"github.com/ilyaluk/girabot/internal/tokenserver"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	dbPath    = flag.String("db-path", "gira-tokens.db", "path to the SQLite database")
	bind      = flag.String("bind", ":8080", "address to bind")
	urlPrefix = flag.String("url-prefix", "/girabot_tokens", "URL prefix for the server")
)

func main() {
	flag.Parse()

	db, err := gorm.Open(sqlite.Open(*dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := db.AutoMigrate(&IntegrityToken{}); err != nil {
		log.Fatal(err)
	}

	s := &server{
		db:   db,
		auth: giraauth.New(http.DefaultClient),
	}

	http.HandleFunc("/stats", s.handleStats)
	http.HandleFunc("/post", s.handlePostToken)
	http.HandleFunc("/exchange", s.handleExchangeToken)
	http.HandleFunc("/exchangeEnc", s.handleExchangeTokenEncrypted)

	log.Println("Starting server on", *bind)
	http.ListenAndServe(*bind, http.StripPrefix(*urlPrefix, http.DefaultServeMux))
}

type IntegrityToken struct {
	Token      string
	CreatedAt  time.Time
	ExpiresAt  time.Time // Can be deducted from token, but for simplicity we store it
	AssignedTo string    `gorm:"index"` // User's sub, verified upon assignment
	AssignedAt time.Time
	UserAgent  string
}

type server struct {
	db   *gorm.DB
	auth *giraauth.Client
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	// Require any token to get stats, even old ones
	token := r.Header.Get("x-firebase-token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	var found int64 = 0
	s.db.Model(&IntegrityToken{}).Where("token = ?", token).Count(&found)
	if found == 0 {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	var stats tokenserver.Stats

	s.db.Model(&IntegrityToken{}).Count(&stats.TotalTokens)
	s.db.Model(&IntegrityToken{}).Where("assigned_to = '' AND expires_at < ?", time.Now()).Count(&stats.ExpiredUnassigned)

	s.db.Model(&IntegrityToken{}).Where("expires_at > ?", time.Now()).Count(&stats.ValidTokens)

	s.db.Model(&IntegrityToken{}).Where("assigned_to = '' AND expires_at > ?", time.Now()).Count(&stats.AvailableTokens)
	// Count tokens that will be available after a 10-minute period
	s.db.Model(&IntegrityToken{}).Where("assigned_to = '' AND expires_at > ?", time.Now().Add(10*time.Minute)).Count(&stats.AvailableTokensAfter10Mins)

	s.db.Model(&IntegrityToken{}).Where("assigned_to != '' AND expires_at > ?", time.Now()).Count(&stats.AssignedTokens)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(stats)
}

func (s *server) handlePostToken(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("x-firebase-token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	exp, err := firebasetoken.GetExpiration(token)
	if err != nil {
		http.Error(w, "bad token", http.StatusBadRequest)
		return
	}

	if exp.Before(time.Now()) {
		http.Error(w, "invalid token", http.StatusBadRequest)
		return
	}

	var existingToken IntegrityToken
	if result := s.db.Where("token = ?", token).First(&existingToken); result.Error == nil {
		http.Error(w, "token already exists", http.StatusConflict)
		return
	}

	log.Printf("got token (valid until %v): %v", exp, token)

	if err := s.db.Create(&IntegrityToken{
		Token:     token,
		CreatedAt: time.Now(),
		ExpiresAt: exp,
	}).Error; err != nil {
		log.Printf("failed to save token: %v", err)
		http.Error(w, "failed to save token", http.StatusInternalServerError)
		return
	}

	w.Write([]byte("thanks!"))
}

func (s *server) handleExchangeToken(w http.ResponseWriter, r *http.Request) {
	token, err := s.getIntegrityToken(r)
	if errors.Is(err, noTokensError) {
		http.Error(w, "no tokens available", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to get token", http.StatusInternalServerError)
		return
	}

	w.Write([]byte(token))
}

func (s *server) handleExchangeTokenEncrypted(w http.ResponseWriter, r *http.Request) {
	integrityToken, err := s.getIntegrityToken(r)
	if errors.Is(err, noTokensError) {
		http.Error(w, "no tokens available", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to get token", http.StatusInternalServerError)
		return
	}

	// We know it's okay-ish for from getIntegrityToken
	giraToken := r.Header.Get("x-gira-token")

	enc, err := firebasetoken.Encrypt(integrityToken, giraToken)
	if err != nil {
		log.Printf("failed to encrypt token: %v", err)
		http.Error(w, "failed to encrypt token", http.StatusInternalServerError)
		return
	}

	w.Write([]byte(enc))
}

var noTokensError = fmt.Errorf("no tokens available")

func (s *server) getIntegrityToken(r *http.Request) (string, error) {
	token := r.Header.Get("x-gira-token")
	if token == "" {
		return "", fmt.Errorf("missing token")
	}

	// First, blindly parse auth token to get "sub". If we have a valid integrity
	// token for this user, just return it.
	// Access tokens are 2minutes long, calling auth api for each one is slow.
	jwtToken, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("bad token")
	}

	sub, err := jwtToken.Claims.GetSubject()
	if err != nil {
		return "", fmt.Errorf("bad token")
	}

	// Add leeway to match auth token lifetime. This adds some wasted firebase
	// tokens, but makes UX more stable for users.
	nowLeeway := time.Now().Add(2 * time.Minute)

	// Check if integrity token is already assigned to a user
	var tok IntegrityToken
	if s.db.Where("assigned_to = ? AND expires_at > ?", sub, nowLeeway).First(&tok).Error == nil {
		log.Printf("got token for user %s (unverified)", sub)

		return tok.Token, nil
	}

	// The user doesn't have active integrity token, so we need to verify auth token
	id, err := s.auth.UserID(r.Context(), token)
	if err != nil {
		log.Printf("failed to get user ID: %v", err)
		return "", fmt.Errorf("failed to get user ID")
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("assigned_to = ? AND expires_at > ?", id, nowLeeway).First(&tok)
		if res.Error == nil {
			// User already has a valid token, use it
			// Should be rare if serving multiple requests for the same user
			return nil
		}

		// No existing token found, allocate a new one
		result := tx.Where("assigned_to = ? AND expires_at > ?", "", time.Now()).
			Order("expires_at ASC").
			First(&tok)

		if result.Error != nil {
			return result.Error
		}

		return tx.Model(&IntegrityToken{}).
			Where("token = ?", tok.Token).
			Update("assigned_to", id).
			Update("assigned_at", time.Now()).
			Update("user_agent", r.UserAgent()).
			Error
	})

	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("no tokens available for %v", id)
		return "", noTokensError
	}

	if err != nil {
		log.Printf("failed to get/assign token: %v", err)
		return "", fmt.Errorf("failed to get/assign token")
	}

	log.Printf("got token for user %s (verified)", id)
	return tok.Token, nil
}
