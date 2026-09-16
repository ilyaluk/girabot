package gira

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ilyaluk/girabot/internal/vaimoo"
)

func TestConvertSubscription(t *testing.T) {
	expired := true
	active := false

	tests := []struct {
		name       string
		usage      vaimoo.SubscriptionUsage
		wantActive bool
	}{
		{
			name:       "expiry in the future",
			usage:      vaimoo.SubscriptionUsage{ExpirationDate: "2099-09-15T10:38:39.343Z"},
			wantActive: true,
		},
		{
			name:       "expiry in the past",
			usage:      vaimoo.SubscriptionUsage{ExpirationDate: "2020-09-15T10:38:39.343Z"},
			wantActive: false,
		},
		{
			name:       "no zone on the expiry",
			usage:      vaimoo.SubscriptionUsage{ExpirationDate: "2099-09-15T10:38:39.343"},
			wantActive: true,
		},
		{
			// A pass the backend says is gone is gone, whatever the date reads.
			name:       "flag beats the date",
			usage:      vaimoo.SubscriptionUsage{ExpirationDate: "2099-09-15T10:38:39.343Z", IsExpired: &expired},
			wantActive: false,
		},
		{
			name:       "flag beats a past date too",
			usage:      vaimoo.SubscriptionUsage{ExpirationDate: "2020-09-15T10:38:39.343Z", IsExpired: &active},
			wantActive: true,
		},
		{
			// An unreadable date must not read as an expired pass.
			name:       "unreadable expiry",
			usage:      vaimoo.SubscriptionUsage{ExpirationDate: "whenever"},
			wantActive: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convertSubscription(tt.usage); got.Active != tt.wantActive {
				t.Errorf("Active = %v, want %v", got.Active, tt.wantActive)
			}
		})
	}
}

func TestConvertError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "nil", err: nil, want: nil},
		{
			name: "refused trip",
			err:  &vaimoo.Error{Status: http.StatusConflict, Message: "already_active_trip"},
			want: ErrAlreadyHasActiveTrip,
		},
		{
			name: "key inside an exception string",
			err:  &vaimoo.Error{Status: 400, Message: "Exception of type 'bike_in_repair' was thrown."},
			want: ErrBikeInRepair,
		},
		{
			name: "forbidden",
			err:  &vaimoo.Error{Status: http.StatusForbidden, Message: "nope"},
			want: ErrForbidden,
		},
		{
			name: "service unavailable",
			err:  &vaimoo.Error{Status: http.StatusServiceUnavailable},
			want: ErrServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertError(tt.err)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	// An error the backend words in some way we don't know reaches the caller.
	unknown := &vaimoo.Error{Status: 400, Message: "something new"}
	if got := convertError(unknown); !errors.Is(got, unknown) {
		t.Errorf("got %v, want the original error", got)
	}
}

func TestCorrectedBikeCount(t *testing.T) {
	stationCacheMu.Lock()
	defer stationCacheMu.Unlock()
	defer func() { observedBikes = map[StationSerial]observedCount{} }()

	station := Station{Serial: "4551", Bikes: 3}

	// Without an observation the published counter stands.
	if got := corrected(station).Bikes; got != 3 {
		t.Errorf("Bikes = %d, want 3", got)
	}

	// Listing the station found only two bikes a rider can take.
	observedBikes["4551"] = observedCount{serverBikes: 3, bikes: 2}
	if got := corrected(station).Bikes; got != 2 {
		t.Errorf("Bikes = %d, want the observed 2", got)
	}

	// Once the published counter moves, the observation is stale.
	station.Bikes = 4
	if got := corrected(station).Bikes; got != 4 {
		t.Errorf("Bikes = %d, want the fresh 4", got)
	}
}

func TestDockOrder(t *testing.T) {
	bikes := Bikes{
		{Name: "E1", DockNumber: 12},
		{Name: "E2", DockNumber: 0},
		{Name: "E3", DockNumber: 2},
	}

	if dockOrder(bikes[1]) <= dockOrder(bikes[0]) {
		t.Error("a bike with no dock should sort after one with a dock")
	}
	if dockOrder(bikes[2]) >= dockOrder(bikes[0]) {
		t.Error("dock 2 should sort before dock 12")
	}
}

func TestTripUpdatePrettyDuration(t *testing.T) {
	start := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		trip TripUpdate
		want string
	}{
		{
			name: "minutes",
			trip: TripUpdate{StartDate: start, EndDate: start.Add(9*time.Minute + 5*time.Second)},
			want: "09:05",
		},
		{
			name: "hours",
			trip: TripUpdate{StartDate: start, EndDate: start.Add(2*time.Hour + 3*time.Minute)},
			want: "2:03:00",
		},
		{
			// Neither clock skew nor a date the backend never sent may render
			// as a trip of several million hours.
			name: "end before start",
			trip: TripUpdate{StartDate: start, EndDate: start.Add(-time.Hour)},
			want: "00:00",
		},
		{
			name: "no start date",
			trip: TripUpdate{EndDate: start},
			want: "00:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.trip.PrettyDuration(); got != tt.want {
				t.Errorf("PrettyDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}
