package gira

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hasura/go-graphql-client"
	"github.com/hasura/go-graphql-client/pkg/jsonutil"
)

func SubscribeServerDate(ctx context.Context, accessToken string) (<-chan time.Time, error) {
	// TODO: fix types and variables
	q := fmt.Sprintf(`subscription {
		serverDate(_access_token: "%s") {
			date
		}
	}`, accessToken)

	ch := make(chan time.Time, 16)

	type serverTime struct {
		ServerDate struct {
			Date string
		}
	}
	cb := func(msg serverTime, err error) error {
		log.Printf("server date: %+v, err: %v", msg, err)
		return err
	}

	return ch, runSubscription(ctx, q, cb)
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

func SubscribeActiveTrip(ctx context.Context, accessToken string) (<-chan TripDetailSub, error) {
	// TODO: fix types and variables
	q := fmt.Sprintf(`subscription {
		activeTripSubscription(_access_token: "%s") {
			code
			bike
			startDate
			endDate
			cost
			finished
			canceled
			canPayWithMoney
			canUsePoints
			clientPoints
			tripPoints
			period
			periodTime
			error
		}
	}`, accessToken)

	ch := make(chan TripDetailSub, 16)

	type activeTrip struct {
		ActiveTripSubscription TripDetailSub
	}
	cb := func(msg activeTrip, err error) error {
		log.Printf("active trip detail: %+v, err: %v", msg, err)
		return err
	}

	return ch, runSubscription(ctx, q, cb)
}

func runSubscription[T any](ctx context.Context, query string, cb func(T, error) error) error {
	c := graphql.NewSubscriptionClient("wss://apigira.emel.pt/graphql").
		//WithLog(log.Println).
		WithTimeout(10 * time.Second) // this is also reconnect interval

	if _, err := c.Exec(query, nil, func(msg []byte, err error) error {
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
	}); err != nil {
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
