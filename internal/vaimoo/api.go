package vaimoo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// User returns the account of the logged in rider.
func (c *Client) User(ctx context.Context, s *Session) (*User, error) {
	var res User
	err := c.call(ctx, "user", options{
		token:  s.AccessToken,
		userID: s.UserID,
		params: map[string]string{"IncludeUserAppSettings": "true"},
	}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// CurrentTrip returns the rider's live trip state. ActiveTripID is zero when
// there is no trip.
func (c *Client) CurrentTrip(ctx context.Context, s *Session) (*CurrentTrip, error) {
	var res CurrentTrip
	err := c.call(ctx, "user/trip", options{
		token:  s.AccessToken,
		userID: s.UserID,
	}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// Trips returns one page of the rider's finished trips, newest first. Pages are
// numbered from one.
func (c *Client) Trips(ctx context.Context, s *Session, page, pageSize int) ([]TripDetails, error) {
	query, err := json.Marshal(map[string]any{
		"pageIndex": page,
		"pageSize":  pageSize,
		"filter":    map[string]any{"filters": []any{}},
	})
	if err != nil {
		return nil, fmt.Errorf("vaimoo: marshaling trip query: %w", err)
	}

	var res struct {
		Data []TripDetails `json:"data"`
	}
	err = c.call(ctx, "trip/trips", options{
		token:  s.AccessToken,
		userID: s.UserID,
		params: map[string]string{"query": string(query)},
	}, &res)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// TripDetails returns one finished trip by its id.
func (c *Client) TripDetails(ctx context.Context, s *Session, tripID int64) (*TripDetails, error) {
	var res TripDetails
	err := c.call(ctx, fmt.Sprintf("trip/trip-details/%d", tripID), options{
		token:  s.AccessToken,
		userID: s.UserID,
	}, &res)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// SubmitFeedback rates a finished trip.
func (c *Client) SubmitFeedback(ctx context.Context, s *Session, f Feedback) error {
	return c.call(ctx, "user-feedback", options{
		method: http.MethodPost,
		token:  s.AccessToken,
		userID: s.UserID,
		body:   f,
	}, nil)
}

// SubscriptionUsage returns the rider's subscriptions, expired ones included.
func (c *Client) SubscriptionUsage(ctx context.Context, s *Session) ([]SubscriptionUsage, error) {
	var res []SubscriptionUsage
	err := c.call(ctx, "subscription/v2/usage", options{
		token:  s.AccessToken,
		userID: s.UserID,
	}, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// RemainingCredit returns the balance of the rider's wallet, in euro.
func (c *Client) RemainingCredit(ctx context.Context, s *Session) (float64, error) {
	var res struct {
		RemainingCredit float64 `json:"remainingCredit"`
	}
	err := c.call(ctx, "wallet/remaining-credit", options{
		token:  s.AccessToken,
		userID: s.UserID,
	}, &res)
	if err != nil {
		return 0, err
	}
	return res.RemainingCredit, nil
}

// QuickStart unlocks a bike and starts a trip on it. The bike is named by its
// communication id, not by the plate printed on its frame.
func (c *Client) QuickStart(ctx context.Context, s *Session, communicationID string) error {
	return c.call(ctx, "trip/v2/quick-start/"+url.PathEscape(communicationID), options{
		method: http.MethodPost,
		token:  s.AccessToken,
		userID: s.UserID,
	}, nil)
}

// LocalTimestamp renders a time the way VAIMOO expects it in a feedback
// payload: local wall clock, no zone.
func LocalTimestamp(t time.Time) string {
	return t.Format(localTimeLayout)
}

const localTimeLayout = "2006-01-02T15:04:05.999999999"

// ParseTime reads a timestamp from the API. Most carry a zone, some come back
// in the same zone-less form the API asks for, and those are read as UTC.
func ParseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("vaimoo: empty timestamp")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(localTimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("vaimoo: unrecognised timestamp %q", s)
	}
	return t.UTC(), nil
}
