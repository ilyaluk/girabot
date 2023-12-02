package gira

import (
	"context"
	"log"
	"time"

	"github.com/hasura/go-graphql-client"
	"github.com/hasura/go-graphql-client/pkg/jsonutil"
	"golang.org/x/oauth2"
)

type Result[T any] struct {
	Value T
	Err   error
}

func SubscribeServerDate(ctx context.Context, ts oauth2.TokenSource) (<-chan Result[time.Time], error) {
	type qType struct {
		ServerDate struct {
			Date string
		} `graphql:"serverDate(_access_token: $token)"`
	}

	ch := make(chan Result[time.Time], 16)

	cb := func(msg qType, err error) error {
		log.Printf("server date: %+v, err: %v", msg, err)
		return err
	}

	return ch, startSubscription(ctx, qType{}, ts, cb)
}

// TODO: make this internal
type TripDetailSub struct {
	Code            string
	Bike            string
	StartDate       string
	EndDate         string
	Cost            float64
	Finished        bool
	Canceled        bool
	CanPayWithMoney bool
	CanUsePoints    bool
	ClientPoints    int
	TripPoints      int
	Period          string
	PeriodTime      string
	Error           int
}

func SubscribeCurrentTrip(ctx context.Context, ts oauth2.TokenSource) (<-chan Result[TripDetailSub], error) {
	type qType struct {
		TripDetail TripDetailSub `graphql:"activeTripSubscription(_access_token: $token)"`
	}

	ch := make(chan Result[TripDetailSub], 16)

	cb := func(msg qType, err error) error {
		log.Printf("active trip detail: %+v, err: %v", msg, err)
		return err
	}

	return ch, startSubscription(ctx, qType{}, ts, cb)
}

func startSubscription[T any](ctx context.Context, query any, ts oauth2.TokenSource, cb func(T, error) error) error {
	tok, err := ts.Token()
	if err != nil {
		return err
	}

	handler := func(msg []byte, err error) error {
		var val T
		// TODO: check for fucking INVALID_OPERATION
		if err != nil {
			return cb(val, err)
		}
		if err := jsonutil.UnmarshalGraphQL(msg, &val); err != nil {
			log.Println("subscription unmarshal error:", err, string(msg))
			return err
		}
		return cb(val, err)
	}

	err = startOneSubscription(ctx, query, tok.AccessToken, handler)
	if err != nil {
		return err
	}

	return nil
}

func startOneSubscription(ctx context.Context, query any, token string, handler func([]byte, error) error) error {
	c := graphql.NewSubscriptionClient("wss://apigira.emel.pt/graphql").
		WithLog(log.Println).
		WithTimeout(10 * time.Second) // this is also reconnect interval

	if _, err := c.Subscribe(query, map[string]any{"token": token}, handler); err != nil {
		log.Println("subscription create error:", err)
		return err
	}

	go func() {
		if err := c.Run(); err != nil {
			log.Println("subscription run error:", err)
		}
	}()

	go func() {
		<-ctx.Done()
		if err := c.Close(); err != nil {
			log.Println("subscription close error:", err)
		}
	}()
	return nil
}
