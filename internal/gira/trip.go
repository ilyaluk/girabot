package gira

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/ilyaluk/girabot/internal/firestore"
	"github.com/ilyaluk/girabot/internal/vaimoo"
)

const (
	// tripPendingInterval is how often the backend is asked about a trip that
	// has not started yet, tripActiveInterval how often about a running one.
	tripPendingInterval = 3 * time.Second
	tripActiveInterval  = 15 * time.Second

	// tripStartTimeout is how long a bike gets to report a started trip before
	// the unlock counts as failed.
	tripStartTimeout = 2 * time.Minute

	// tripMaxPollErrors is how many failed polls in a row end the watch. The
	// backend is flaky enough that a single failure means nothing.
	tripMaxPollErrors = 20

	// bikeStateLocked is the lock state a bike reports once it is docked.
	bikeStateLocked = "LOCKED"
)

const (
	// TripErrorStartTimeout is the code a bike reports when an unlock timed out
	// on its side.
	TripErrorStartTimeout = 100
	// TripErrorNotStarted is reported when no trip appeared after an unlock.
	TripErrorNotStarted = -1
	// TripErrorWatchFailed is reported when the trip can no longer be followed.
	TripErrorWatchFailed = -2
)

var (
	tripWatchesCnt = promauto.NewCounter(prometheus.CounterOpts{Name: "gira_trip_watches_total"})
	tripPollsCnt   = promauto.NewCounter(prometheus.CounterOpts{Name: "gira_trip_polls_total"})
	tripErrorsCnt  = promauto.NewCounter(prometheus.CounterOpts{Name: "gira_trip_poll_errors_total"})
)

// WatchTrip follows the rider's trip until it ends and reports what it sees.
//
// The backend has no push channel, so the trip endpoint is polled, and that
// endpoint is the only thing allowed to decide whether a trip is running. The
// bike publishes its own state to Firestore sooner, which is used to poll
// harder around the moment it looks like the ride is over.
//
// bikeHint names the bike that was just unlocked, resumeCode the trip a restart
// left behind. The last update on the returned channel is always either a
// finished trip or an ErrorCode, and the channel is closed after it.
func (c *Client) WatchTrip(ctx context.Context, bikeHint string, resumeCode TripCode) <-chan TripUpdate {
	tripWatchesCnt.Inc()
	ch := make(chan TripUpdate, 16)

	go func() {
		defer close(ch)
		c.watchTrip(ctx, bikeHint, resumeCode, ch)
	}()

	return ch
}

func (c *Client) watchTrip(ctx context.Context, bikeHint string, resumeCode TripCode, ch chan<- TripUpdate) {
	send := func(u TripUpdate) {
		select {
		case ch <- u:
		case <-ctx.Done():
		}
	}

	bike := bikeHint
	watchStart := time.Now()

	var (
		confirmed    bool
		endSignalled bool
		code         TripCode
		startDate    time.Time
		pollErrors   int
		bikeErrors   int

		// A bike keeps the error code of whoever rode it last, so only a change
		// seen while watching says anything about this trip.
		bikeErrorCode    *int
		bikeErrorKnown   bool
		bikeErrorChanged bool
	)

	for {
		tripPollsCnt.Inc()

		if bike != "" {
			doc, found, err := c.getBikeDoc(ctx, bike)
			switch {
			case err != nil:
				bikeErrors++
				if bikeErrors == 1 {
					log.Printf("gira: bike %s lookup failed: %v", bike, err)
				}
			case found:
				bikeErrors = 0
				if bikeErrorKnown && !sameErrorCode(bikeErrorCode, doc.TripErrorCode) && doc.TripErrorCode != nil {
					log.Printf("gira: bike %s reported error code %d", bike, *doc.TripErrorCode)
					bikeErrorChanged = true
				}
				bikeErrorCode, bikeErrorKnown = doc.TripErrorCode, true

				endSignalled = confirmed && doc.TripVehicleState != nil &&
					*doc.TripVehicleState == bikeStateLocked && doc.TripId == nil
			}
		}

		trip, err := c.GetActiveTrip(ctx)
		switch {
		case err == nil:
			pollErrors = 0
			confirmed = true
			bikeErrorChanged = false
			code = trip.Code
			if trip.BikeName != "" {
				bike = trip.BikeName
			}
			if !trip.StartDate.IsZero() {
				startDate = trip.StartDate
			} else if startDate.IsZero() {
				startDate = watchStart
			}

			send(TripUpdate{Code: code, Bike: bike, StartDate: startDate})

		case errors.Is(err, ErrNoActiveTrip):
			pollErrors = 0
			switch {
			case confirmed:
				log.Printf("gira: trip %s ended", code)
				send(c.finishedTrip(ctx, code, bike, startDate))
				return

			case resumeCode != "":
				log.Printf("gira: trip %s ended while the bot was away", resumeCode)
				send(c.finishedTrip(ctx, resumeCode, bike, startDate))
				return

			case bikeErrorChanged && bikeErrorCode != nil:
				log.Printf("gira: bike %s failed to start a trip, code %d", bike, *bikeErrorCode)
				send(TripUpdate{Bike: bike, ErrorCode: *bikeErrorCode})
				return

			case time.Since(watchStart) > tripStartTimeout:
				log.Printf("gira: no trip started for bike %s within %v", bike, tripStartTimeout)
				send(TripUpdate{Bike: bike, ErrorCode: TripErrorNotStarted})
				return
			}

		case errors.Is(err, ErrNotLoggedIn), errors.Is(err, vaimoo.ErrInvalidRefreshToken):
			log.Printf("gira: stopping trip watch: %v", err)
			send(TripUpdate{Code: code, Bike: bike, ErrorCode: TripErrorWatchFailed})
			return

		default:
			tripErrorsCnt.Inc()
			pollErrors++
			log.Printf("gira: trip poll failed (%d in a row): %v", pollErrors, err)
			if pollErrors >= tripMaxPollErrors {
				log.Printf("gira: giving up on trip %s after %d failed polls", code, pollErrors)
				send(TripUpdate{Code: code, Bike: bike, ErrorCode: TripErrorWatchFailed})
				return
			}
		}

		interval := tripPendingInterval
		if confirmed && !endSignalled {
			interval = tripActiveInterval
		}

		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return
		}
	}
}

func sameErrorCode(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// finishedTrip builds the closing update, filling in cost and distance from the
// trip history once the backend has filed the trip there.
func (c *Client) finishedTrip(ctx context.Context, code TripCode, bike string, startDate time.Time) TripUpdate {
	res := TripUpdate{
		Code:      code,
		Bike:      bike,
		Finished:  true,
		StartDate: startDate,
		EndDate:   time.Now(),
	}

	trips, err := c.GetTripHistory(ctx, 1, 10)
	if err != nil {
		log.Printf("gira: could not read trip history for %s: %v", code, err)
		return res
	}

	for _, t := range trips {
		if t.Code != code {
			continue
		}
		res.Cost = t.Cost
		res.Distance = t.Distance
		if !t.StartDate.IsZero() {
			res.StartDate = t.StartDate
		}
		if !t.EndDate.IsZero() {
			res.EndDate = t.EndDate
		}
		if t.BikeName != "" {
			res.Bike = t.BikeName
		}
		return res
	}

	log.Printf("gira: trip %s is not in the recent history yet", code)
	return res
}

// getBikeDoc reads one bike document by the plate printed on its frame.
func (c *Client) getBikeDoc(ctx context.Context, name string) (innerBike, bool, error) {
	var inner []innerBike
	err := c.fs.Query(ctx, bikesCollection, []firestore.Filter{
		firestore.Equal("Tenant", vaimoo.Tenant),
		firestore.Equal("VisualId", strings.ToUpper(strings.TrimSpace(name))),
	}, &inner)
	if err != nil {
		return innerBike{}, false, err
	}
	if len(inner) == 0 {
		return innerBike{}, false, nil
	}
	return inner[0], true, nil
}
