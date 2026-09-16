package vaimoo

import "time"

// Session is an authenticated VAIMOO session. It is persisted by the bot, so
// its JSON field names are part of the on-disk format.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`

	// Expiry is when the access token stops working, five minutes after it was
	// issued. RefreshExpiry is when the refresh token does, five days after.
	Expiry        time.Time `json:"expiry"`
	RefreshExpiry time.Time `json:"refresh_expiry"`

	UserID int64 `json:"user_id"`
}

// Valid reports whether the session has an access token that is still good.
func (s *Session) Valid() bool {
	if s == nil || s.AccessToken == "" {
		return false
	}
	// A request still has to arrive before the token expires.
	return time.Now().Add(30 * time.Second).Before(s.Expiry)
}

// RefreshValid reports whether the refresh token is still worth trying.
func (s *Session) RefreshValid() bool {
	if s == nil || s.RefreshToken == "" {
		return false
	}
	return s.RefreshExpiry.IsZero() || time.Now().Before(s.RefreshExpiry)
}

// accessToken is the token pair as the API returns it.
type accessToken struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	// ExpireSeconds is absent from the login answer, which is why the lifetime
	// is read out of the token itself first.
	ExpireSeconds string `json:"expireSeconds"`
}

// loginResponse is the answer to both the code exchange and the refresh call.
type loginResponse struct {
	AccessToken accessToken `json:"accessToken"`
	User        User        `json:"user"`
}

// User is the account of the logged in rider.
type User struct {
	UserID int64 `json:"userId"`
	// The refresh answer names the same field id.
	ID int64 `json:"id"`

	UserName  string `json:"userName"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Email     string `json:"email"`
}

// CurrentTrip is the live state of the rider's trip, empty when there is none.
type CurrentTrip struct {
	ActiveTripID     int64   `json:"activeTripId"`
	VisualID         *string `json:"visualId"`
	TripStartDate    *string `json:"tripStartDate"`
	BikePcbBikeState *string `json:"bikePcbBikeState"`
}

// TripStation is a station as named in a trip record.
type TripStation struct {
	Name      string `json:"name"`
	StationID *int64 `json:"stationId"`
}

// TripDetails is one finished trip.
type TripDetails struct {
	TripID                  *int64       `json:"tripId"`
	StartDate               string       `json:"startDate"`
	EndDate                 string       `json:"endDate"`
	CoveredDistanceInMeters float64      `json:"coveredDistanceInMeters"`
	StartStation            *TripStation `json:"startStation"`
	EndStation              *TripStation `json:"endStation"`
	TripCost                float64      `json:"tripCost"`

	Vehicle *struct {
		VisualID string `json:"visualId"`
	} `json:"vehicle"`
}

// Feedback is a trip rating, shaped the way the official app submits one.
type Feedback struct {
	CreateDate      string   `json:"createDate"`
	OsVersion       string   `json:"osVersion"`
	AppVersion      string   `json:"appVersion"`
	Rating          int      `json:"rating"`
	Comment         []string `json:"comment"`
	ReportType      string   `json:"reportType"`
	VehicleVisualID string   `json:"vehicleVisualId"`
	GeoFenceID      *int64   `json:"geoFenceId"`
	TripID          int64    `json:"tripId"`
}

// SubscriptionUsage is one subscription the rider holds.
type SubscriptionUsage struct {
	ExpirationDate string `json:"expirationDate"`
	IsExpired      *bool  `json:"isExpired"`

	CurrentSubscription struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"currentSubscription"`
}
