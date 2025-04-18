package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ilyaluk/girabot/internal/firebasetoken"
	"github.com/ilyaluk/girabot/internal/giraauth"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var (
	dbPath = flag.String("db-path", "gira-tokens.db", "path to the SQLite database")
	bind   = flag.String("bind", ":8080", "address to bind")
)

func main() {
	flag.Parse()

	db, err := gorm.Open(sqlite.Open(*dbPath), &gorm.Config{})
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

	http.HandleFunc("/post", s.handlePostToken)
	http.HandleFunc("/exchange", s.handleExchangeToken)

	log.Println("Starting server on", *bind)
	http.ListenAndServe(*bind, nil)
}

type IntegrityToken struct {
	Token      string
	CreatedAt  time.Time
	ExpiresAt  time.Time // Can be deducted from token, but for simplicity we store it
	AssignedTo string    `gorm:"index"` // User's sub, verified upon assignment
	AssignedAt time.Time
}

type server struct {
	db   *gorm.DB
	auth *giraauth.Client
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
	token := r.Header.Get("x-gira-token")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	// First, blindly parse auth token to get "sub". If we have a valid integrity
	// token for this user, just return it.
	// Access tokens are 2minutes long, calling auth api for each one is slow.
	jwtToken, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		http.Error(w, "bad token", http.StatusBadRequest)
		return
	}

	sub, err := jwtToken.Claims.GetSubject()
	if err != nil {
		http.Error(w, "bad token", http.StatusBadRequest)
		return
	}

	nowLeeway := time.Now().Add(10 * time.Second)

	// Check if integrity token is already assigned to a user
	var tok IntegrityToken
	if s.db.Where("assigned_to = ? AND expires_at > ?", sub, nowLeeway).First(&tok).Error == nil {
		log.Printf("got token for user %s (unverified)", sub)
		w.Write([]byte(tok.Token))
		return
	}

	// The user doesn't have active integrity token, so we need to verify auth token
	id, err := s.auth.UserID(r.Context(), token)
	if err != nil {
		log.Printf("failed to get user ID: %v", err)
		http.Error(w, "bad token", http.StatusBadRequest)
		return
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
			Update("assigned_to", id).Update("assigned_at", time.Now()).
			Error
	})
	if err != nil {
		log.Printf("failed to get/assign token: %v", err)
		http.Error(w, "failed to get token", http.StatusInternalServerError)
		return
	}

	log.Printf("got token for user %s", id)
	w.Write([]byte(tok.Token))
}
