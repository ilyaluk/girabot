package main

import (
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/oauth2"
	tele "gopkg.in/telebot.v3"

	"github.com/ilyaluk/girabot/internal/gira"
)

//go:embed webapp/index.html
var indexHTML []byte

var staticServer = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write(indexHTML)
})

func (s *server) handleWebStations(w http.ResponseWriter, r *http.Request) {
	uid, err := s.validateTgUserId(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var user User
	s.db.First(&user, uid)

	girac := gira.New(oauth2.NewClient(r.Context(), s.getTokenSource(uid)))

	stations, err := girac.GetStations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type respStation struct {
		Number  string  `json:"number"`
		Lat     float64 `json:"lat"`
		Lng     float64 `json:"lng"`
		Bikes   int     `json:"bikes"`
		Docks   int     `json:"docks"`
		Status  string  `json:"status"`
		FavName string  `json:"fav_name,omitempty"`
	}
	resp := make([]respStation, len(stations))

	for i, station := range stations {
		status := "active"
		if station.Status != gira.AssetStatusActive {
			status = "inactive"
		}

		resp[i] = respStation{
			Number:  station.Number(),
			Lat:     station.Latitude,
			Lng:     station.Longitude,
			Bikes:   station.Bikes,
			Docks:   station.Docks,
			Status:  status,
			FavName: user.Favorites[station.Serial],
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *server) handleWebSelectStation(w http.ResponseWriter, r *http.Request) {
	_, err := s.validateTgUserId(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	q := r.URL.Query()
	stationNum := q.Get("number")

	if len(stationNum) > 4 {
		// good enough validation
		http.Error(w, "bad station number", http.StatusBadRequest)
		return
	}

	// Apparently, correct serialization is implemented only on Results type, not on Result
	results := tele.Results{
		&tele.VenueResult{
			ResultBase: tele.ResultBase{
				// Why Venue? Because telegram api is weird, to say the least
				Type: "venue",
				// > Alternatively, you can use input_message_content to send a message with the
				// > specified content instead of the venue.
				// Jesus Christ, telegram api is a mess
				Content: &tele.InputTextMessageContent{
					Text: stationNum,
				},
			},
			// Nope, if we remove title, the query is not answered and for some reason parses as article
			Title: "f",
		},
	}

	resultsBytes, err := json.Marshal(results)
	if err != nil {
		log.Println("error marshalling results:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	params := map[string]interface{}{
		"web_app_query_id": q.Get("query_id"),
		"result":           json.RawMessage(resultsBytes[1 : len(resultsBytes)-1]), // :harold:
	}

	_, err = s.bot.Raw("answerWebAppQuery", params)
	if err != nil {
		log.Println("error answering webapp query:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

var (
	hmacKey     []byte
	hmacKeyOnce sync.Once
)

func (s *server) validateTgUserId(r *http.Request) (int64, error) {
	// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
	hmacKeyOnce.Do(func() {
		h := hmac.New(sha256.New, []byte("WebAppData"))
		h.Write([]byte(s.bot.Token))
		hmacKey = h.Sum(nil)
	})

	q := r.URL.Query()

	dataCheckString := fmt.Sprintf(
		"auth_date=%s\nquery_id=%s\nuser=%s",
		q.Get("auth_date"), q.Get("query_id"), q.Get("user"))

	h := hmac.New(sha256.New, hmacKey)
	h.Write([]byte(dataCheckString))
	expectedHash := h.Sum(nil)

	gotHashHex := q.Get("hash")
	gotHash, err := hex.DecodeString(gotHashHex)
	if err != nil {
		return 0, fmt.Errorf("bad hash")
	}

	if !hmac.Equal(expectedHash, gotHash) {
		return 0, fmt.Errorf("bad hash")
	}

	authDateStr := q.Get("auth_date")
	authDate, err := strconv.ParseInt(authDateStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad auth_date")
	}

	delta := time.Now().Sub(time.Unix(authDate, 0))
	if math.Abs(delta.Minutes()) > 10 {
		return 0, fmt.Errorf("bad auth_date")
	}

	var tgUser struct {
		ID int64
	}

	if err := json.Unmarshal([]byte(q.Get("user")), &tgUser); err != nil {
		return 0, fmt.Errorf("bad user")
	}

	return tgUser.ID, nil
}
