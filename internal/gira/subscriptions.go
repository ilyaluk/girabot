package gira

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/hasura/go-graphql-client"
	"github.com/hasura/go-graphql-client/pkg/jsonutil"
	"golang.org/x/oauth2"
)

func SubscribeServerDate(ctx context.Context, ts oauth2.TokenSource) (<-chan time.Time, error) {
	type qType struct {
		ServerDate struct {
			Date string
		} `graphql:"serverDate(_access_token: $token)"`
	}

	ch := make(chan time.Time, 16)
	startSubscription(ctx, qType{}, ts, func(msg qType) {
		log.Printf("server date: %+v", msg)
		t, _ := time.Parse(time.RFC3339, msg.ServerDate.Date)
		ch <- t
	})

	return ch, nil
}

type TripUpdate struct {
	Code            TripCode
	Bike            string
	StartDate       time.Time
	EndDate         time.Time
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

func SubscribeActiveTrips(ctx context.Context, ts oauth2.TokenSource) (<-chan TripUpdate, error) {
	type tripUpdate struct {
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

	type qType struct {
		TripDetail tripUpdate `graphql:"activeTripSubscription(_access_token: $token)"`
	}

	ch := make(chan TripUpdate, 16)

	cb := func(msg qType) {
		log.Printf("active trip detail: %+v", msg)
		startT, _ := time.Parse(time.RFC3339, msg.TripDetail.StartDate)
		endT, _ := time.Parse(time.RFC3339, msg.TripDetail.EndDate)
		ch <- TripUpdate{
			Code:            TripCode(msg.TripDetail.Code),
			Bike:            msg.TripDetail.Bike,
			StartDate:       startT,
			EndDate:         endT,
			Cost:            msg.TripDetail.Cost,
			Finished:        msg.TripDetail.Finished,
			Canceled:        msg.TripDetail.Canceled,
			CanPayWithMoney: msg.TripDetail.CanPayWithMoney,
			CanUsePoints:    msg.TripDetail.CanUsePoints,
			ClientPoints:    msg.TripDetail.ClientPoints,
			TripPoints:      msg.TripDetail.TripPoints,
			Period:          msg.TripDetail.Period,
			PeriodTime:      msg.TripDetail.PeriodTime,
			Error:           msg.TripDetail.Error,
		}
	}

	startSubscription(ctx, qType{}, ts, cb)
	return ch, nil
}

func startSubscription[T any](ctx context.Context, query any, ts oauth2.TokenSource, cb func(T)) {
	willRetry := true
	handler := func(msg []byte, err error) error {
		var val T
		if err != nil {
			if isInvalidOperationError([]byte(err.Error())) {
				// backend regularly returns this error, retry it
				log.Println("subscription error was INVALID_OPERATION")
				return graphql.ErrSubscriptionStopped
			}
			// other errors are fatal, don't retry
			log.Println("subscription error:", err)
			willRetry = false
			return err
		}
		if err := jsonutil.UnmarshalGraphQL(msg, &val); err != nil {
			log.Println("subscription unmarshal error:", err, string(msg))
			return err
		}
		cb(val)
		return nil
	}

	go func() {
		for willRetry {
			tok, err := ts.Token()
			if err != nil {
				log.Println("subscription token error:", err)
				return
			}

			err = startOneSubscription(ctx, query, tok.AccessToken, handler)
			if err != nil {
				log.Println("subscription error:", err)
				return
			}

			select {
			case <-ctx.Done():
				log.Println("subscription context done, stopping")
				return
			default:
				// do not overload server with retries
				time.Sleep(time.Second + time.Duration(rand.Intn(1000))*time.Millisecond)
			}
		}
	}()
}

func startOneSubscription(ctx context.Context, query any, token string, handler func([]byte, error) error) error {
	c := graphql.NewSubscriptionClient("wss://apigira.emel.pt/graphql")

	if _, err := c.Subscribe(query, map[string]any{"token": token}, handler); err != nil {
		log.Println("subscription create error:", err)
		return err
	}

	go func() {
		<-ctx.Done()
		if err := c.Close(); err != nil {
			log.Println("subscription close error:", err)
		}
	}()

	return c.Run()
}
