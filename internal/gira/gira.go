// Package gira exposes the Gira bike sharing service to the bot.
//
// Since the 2026 migration the service runs on VAIMOO: the account, the wallet
// and the trips come from the VAIMOO API, while station and bike state is
// published in a public Firestore database that the official app reads directly.
package gira

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ilyaluk/girabot/internal/firestore"
	"github.com/ilyaluk/girabot/internal/vaimoo"
)

const (
	// firestoreProject and firestoreAPIKey are the public client credentials of
	// the official VAIMOO app. Gira shares the project with other VAIMOO cities,
	// so every query is scoped to the Gira tenant.
	firestoreProject = "vaimoorotterdam"
	firestoreAPIKey  = "AIzaSyAmKfHdjYUhzYmg7qSZtRwwYE92HQQlmJ4"

	stationsCollection = "docking-stations"
	bikesCollection    = "bikes"
)

var (
	// ErrNoActiveTrip means the rider is not on a bike right now.
	ErrNoActiveTrip = errors.New("gira: no active trip")
	// ErrNotLoggedIn means no session is available for this rider.
	ErrNotLoggedIn = errors.New("gira: not logged in")

	ErrAlreadyHasActiveTrip     = errors.New("gira: already has active trip")
	ErrBikeInRepair             = errors.New("gira: bike in repair")
	ErrNotEnoughBalance         = errors.New("gira: not enough balance")
	ErrTripIntervalLimit        = errors.New("gira: trip interval limit")
	ErrHasNoActiveSubscriptions = errors.New("gira: has no active subscriptions")
	ErrBikeAlreadyInTrip        = errors.New("gira: bike already in trip")
	ErrNoBikeFound              = errors.New("gira: no bike found")
	ErrUnableToStartTrip        = errors.New("gira: unable to start trip")
	ErrTripNotFound             = errors.New("gira: trip not found")
	ErrServiceUnavailable       = errors.New("gira: service unavailable")
	ErrForbidden                = errors.New("gira: forbidden")
)

// SessionSource hands out the VAIMOO session of one rider.
type SessionSource interface {
	// Session returns a session with a usable access token. When force is set,
	// the token is refreshed even if the cached one still looks valid.
	Session(force bool) (*vaimoo.Session, error)
}

// Client is the Gira client of one rider. A client without a session source can
// still read stations and bikes, which the backend publishes to everyone.
type Client struct {
	api  *vaimoo.Client
	fs   *firestore.Client
	sess SessionSource
}

// New returns a client that uses httpc for the VAIMOO API. Firestore gets its
// own client: it is a different operator with its own reliability, and the
// User-Agent the VAIMOO transport sets has no business going to Google.
func New(httpc *http.Client, sess SessionSource) *Client {
	if httpc == nil {
		httpc = &http.Client{}
	}
	return &Client{
		api:  vaimoo.New(httpc),
		fs:   firestore.New(&http.Client{Timeout: 30 * time.Second}, firestoreProject, firestoreAPIKey),
		sess: sess,
	}
}

// withSession runs f with the rider's session, retrying once with a fresh token
// if the backend rejected the one it was given.
func (c *Client) withSession(f func(*vaimoo.Session) error) error {
	if c.sess == nil {
		return ErrNotLoggedIn
	}

	session, err := c.sess.Session(false)
	if err != nil {
		return err
	}

	err = f(session)
	if !errors.Is(err, vaimoo.ErrUnauthorized) {
		return convertError(err)
	}

	session, refreshErr := c.sess.Session(true)
	if refreshErr != nil {
		return refreshErr
	}
	return convertError(f(session))
}

// GetClientInfo returns the rider's account, balance and subscriptions.
func (c *Client) GetClientInfo(ctx context.Context) (ClientInfo, error) {
	var res ClientInfo

	err := c.withSession(func(s *vaimoo.Session) error {
		user, err := c.api.User(ctx, s)
		if err != nil {
			return err
		}

		balance, err := c.api.RemainingCredit(ctx, s)
		if err != nil {
			return err
		}

		usage, err := c.api.SubscriptionUsage(ctx, s)
		if err != nil {
			return err
		}

		name := strings.TrimSpace(user.FirstName + " " + user.LastName)
		if name == "" {
			name = user.UserName
		}

		res = ClientInfo{
			Name:    name,
			Email:   user.Email,
			Balance: balance,
		}
		for _, u := range usage {
			res.ActiveSubscriptions = append(res.ActiveSubscriptions, convertSubscription(u))
		}
		return nil
	})

	return res, err
}

func convertSubscription(u vaimoo.SubscriptionUsage) ClientSubscription {
	expiry, err := vaimoo.ParseTime(u.ExpirationDate)

	// A date the backend words in some way we don't know must not read as an
	// expired pass, which would tell the rider to go and buy another one.
	active := true
	switch {
	case u.IsExpired != nil:
		active = !*u.IsExpired
	case err == nil:
		active = expiry.After(time.Now())
	default:
		log.Printf("gira: subscription %q has an unreadable expiry: %v", u.CurrentSubscription.Name, err)
	}

	return ClientSubscription{
		Name:           u.CurrentSubscription.Name,
		Description:    u.CurrentSubscription.Description,
		Active:         active,
		ExpirationDate: expiry,
	}
}

var (
	stationCacheMu sync.Mutex
	stationCache   = map[StationSerial]Station{}
	observedBikes  = map[StationSerial]observedCount{}
)

// observedCount remembers how many bikes a station really offered when its own
// counter said serverBikes.
type observedCount struct {
	serverBikes int
	bikes       int
}

// corrected returns the station as callers should see it. The bike counter a
// station publishes includes bikes that are out of service, so it can promise
// more than a rider can unlock. Once the bikes of a station have been listed,
// the number actually on offer is preferred, for as long as the published
// counter stays where it was when that observation was made.
//
// stationCache holds stations exactly as published, so the correction is applied
// on the way out rather than on the way in. The caller must hold stationCacheMu.
func corrected(s Station) Station {
	if observed, ok := observedBikes[s.Serial]; ok && observed.serverBikes == s.Bikes {
		s.Bikes = observed.bikes
	}
	return s
}

// GetStations returns every Gira station.
func (c *Client) GetStations(ctx context.Context) ([]Station, error) {
	var inner []innerStation
	err := c.fs.Query(ctx, stationsCollection, []firestore.Filter{
		firestore.Equal("Tenant", vaimoo.Tenant),
	}, &inner)
	if err != nil {
		return nil, err
	}

	res := make([]Station, len(inner))
	for i, s := range inner {
		res[i] = s.export()
	}
	slices.SortFunc(res, func(a, b Station) int {
		return cmp.Compare(a.Number(), b.Number())
	})

	stationCacheMu.Lock()
	defer stationCacheMu.Unlock()
	stationCache = make(map[StationSerial]Station, len(res))
	for i, s := range res {
		stationCache[s.Serial] = s
		res[i] = corrected(s)
	}

	return res, nil
}

// GetStation returns one station with up to date bike and dock counts.
func (c *Client) GetStation(ctx context.Context, serial StationSerial) (Station, error) {
	id, err := strconv.ParseInt(string(serial), 10, 64)
	if err != nil {
		return Station{}, fmt.Errorf("gira: bad station serial %q: %w", serial, err)
	}

	var inner []innerStation
	err = c.fs.Query(ctx, stationsCollection, []firestore.Filter{
		firestore.Equal("Tenant", vaimoo.Tenant),
		firestore.Equal("DockingStationId", id),
	}, &inner)
	if err != nil {
		return Station{}, err
	}
	if len(inner) == 0 {
		return Station{}, fmt.Errorf("gira: station %s not found", serial)
	}

	station := inner[0].export()

	stationCacheMu.Lock()
	defer stationCacheMu.Unlock()
	stationCache[station.Serial] = station

	return corrected(station), nil
}

// GetStationCached returns a station from the cache, falling back to a fresh
// station list. It avoids a round trip when up to date counts are not required.
func (c *Client) GetStationCached(ctx context.Context, serial StationSerial) (Station, error) {
	stationCacheMu.Lock()
	station, ok := stationCache[serial]
	empty := len(stationCache) == 0
	if ok {
		station = corrected(station)
	}
	stationCacheMu.Unlock()

	if ok {
		return station, nil
	}
	if !empty {
		return Station{}, fmt.Errorf("gira: station %s not found in cache", serial)
	}

	if _, err := c.GetStations(ctx); err != nil {
		return Station{}, err
	}

	stationCacheMu.Lock()
	defer stationCacheMu.Unlock()
	station, ok = stationCache[serial]
	if !ok {
		return Station{}, fmt.Errorf("gira: station %s not found", serial)
	}
	return corrected(station), nil
}

// GetStationBikes returns the bikes a rider can unlock at one station, ordered
// by dock number.
func (c *Client) GetStationBikes(ctx context.Context, serial StationSerial) (Bikes, error) {
	id, err := strconv.ParseInt(string(serial), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("gira: bad station serial %q: %w", serial, err)
	}

	var inner []innerBike
	err = c.fs.Query(ctx, bikesCollection, []firestore.Filter{
		firestore.Equal("Tenant", vaimoo.Tenant),
		firestore.Equal("DockingStationId", id),
	}, &inner)
	if err != nil {
		return nil, err
	}

	res := make(Bikes, 0, len(inner))
	for _, b := range inner {
		if !b.available() {
			continue
		}
		res = append(res, b.export())
	}

	slices.SortFunc(res, func(a, b Bike) int {
		if n := cmp.Compare(dockOrder(a), dockOrder(b)); n != 0 {
			return n
		}
		return cmp.Compare(a.Name, b.Name)
	})

	stationCacheMu.Lock()
	if station, ok := stationCache[serial]; ok {
		observedBikes[serial] = observedCount{serverBikes: station.Bikes, bikes: len(res)}
	}
	stationCacheMu.Unlock()

	return res, nil
}

// dockOrder sorts a bike whose dock the backend did not name to the end.
func dockOrder(b Bike) int {
	if b.DockNumber == 0 {
		return math.MaxInt
	}
	return b.DockNumber
}

// GetBike looks a bike up by the plate printed on its frame.
func (c *Client) GetBike(ctx context.Context, name string) (Bike, error) {
	doc, found, err := c.getBikeDoc(ctx, name)
	if err != nil {
		return Bike{}, err
	}
	if !found || !doc.available() {
		return Bike{}, ErrNoBikeFound
	}
	return doc.export(), nil
}

// StartTrip unlocks a bike and starts a trip on it.
func (c *Client) StartTrip(ctx context.Context, commID string) error {
	if commID == "" {
		return ErrNoBikeFound
	}
	return c.withSession(func(s *vaimoo.Session) error {
		return c.api.QuickStart(ctx, s, commID)
	})
}

// GetActiveTrip returns the trip the rider is on, or ErrNoActiveTrip.
func (c *Client) GetActiveTrip(ctx context.Context) (ActiveTrip, error) {
	var res ActiveTrip

	err := c.withSession(func(s *vaimoo.Session) error {
		trip, err := c.api.CurrentTrip(ctx, s)
		if err != nil {
			return err
		}
		if trip.ActiveTripID <= 0 {
			return ErrNoActiveTrip
		}
		res = convertActiveTrip(trip)
		return nil
	})

	return res, err
}

func convertActiveTrip(t *vaimoo.CurrentTrip) ActiveTrip {
	res := ActiveTrip{
		Code: TripCode(strconv.FormatInt(t.ActiveTripID, 10)),
	}
	if t.VisualID != nil {
		res.BikeName = *t.VisualID
	}
	if t.BikePcbBikeState != nil {
		res.BikeState = *t.BikePcbBikeState
	}
	if t.TripStartDate != nil {
		res.StartDate, _ = time.Parse(time.RFC3339, *t.TripStartDate)
	}
	return res
}

// GetTripHistory returns one page of finished trips, newest first. Pages are
// numbered from one.
func (c *Client) GetTripHistory(ctx context.Context, page, pageSize int) ([]Trip, error) {
	var res []Trip

	err := c.withSession(func(s *vaimoo.Session) error {
		trips, err := c.api.Trips(ctx, s, page, pageSize)
		if err != nil {
			return err
		}
		res = make([]Trip, 0, len(trips))
		for _, t := range trips {
			res = append(res, convertTrip(t))
		}
		return nil
	})

	return res, err
}

// GetTrip returns one finished trip by its code.
func (c *Client) GetTrip(ctx context.Context, code TripCode) (Trip, error) {
	id, err := strconv.ParseInt(string(code), 10, 64)
	if err != nil {
		return Trip{}, fmt.Errorf("gira: bad trip code %q: %w", code, err)
	}

	var res Trip
	err = c.withSession(func(s *vaimoo.Session) error {
		trip, err := c.api.TripDetails(ctx, s, id)
		if err != nil {
			return err
		}
		res = convertTrip(*trip)
		// trip-details does not echo the id back on every deployment.
		if res.Code == "" {
			res.Code = code
		}
		return nil
	})

	return res, err
}

func convertTrip(t vaimoo.TripDetails) Trip {
	res := Trip{
		Distance: t.CoveredDistanceInMeters,
		Cost:     t.TripCost,
	}
	if t.TripID != nil && *t.TripID != 0 {
		res.Code = TripCode(strconv.FormatInt(*t.TripID, 10))
	}
	res.StartDate, _ = vaimoo.ParseTime(t.StartDate)
	res.EndDate, _ = vaimoo.ParseTime(t.EndDate)
	if t.Vehicle != nil {
		res.BikeName = t.Vehicle.VisualID
	}
	if t.StartStation != nil {
		res.StartLocationName = t.StartStation.Name
	}
	if t.EndStation != nil {
		res.EndLocationName = t.EndStation.Name
		if t.EndStation.StationID != nil {
			res.EndStation = *t.EndStation.StationID
		}
	}
	return res
}

// RateTrip submits a rating for a finished trip. The bike plate is part of the
// payload, so the caller has to remember which bike the trip was on.
func (c *Client) RateTrip(ctx context.Context, code TripCode, bikeName string, rating TripRating) error {
	id, err := strconv.ParseInt(string(code), 10, 64)
	if err != nil {
		return fmt.Errorf("gira: bad trip code %q: %w", code, err)
	}
	if rating.Rating < 1 || rating.Rating > 5 {
		return fmt.Errorf("gira: rating %d out of range", rating.Rating)
	}

	// The lookup runs in its own session scope, so that a retried submission
	// after an expired token cannot repeat a rating the backend already took.
	var geoFence *int64
	err = c.withSession(func(s *vaimoo.Session) error {
		details, err := c.api.TripDetails(ctx, s, id)
		if err != nil {
			return err
		}
		if details.EndStation != nil {
			geoFence = details.EndStation.StationID
		}
		return nil
	})
	if err != nil {
		log.Printf("gira: rating trip %s without its end station: %v", code, err)
	}

	return c.withSession(func(s *vaimoo.Session) error {
		return c.api.SubmitFeedback(ctx, s, vaimoo.Feedback{
			CreateDate:      vaimoo.LocalTimestamp(time.Now()),
			OsVersion:       "Android",
			AppVersion:      "1.0.0",
			Rating:          rating.Rating,
			Comment:         []string{rating.Comment},
			ReportType:      "Opinion",
			VehicleVisualID: bikeName,
			GeoFenceID:      geoFence,
			TripID:          id,
		})
	})
}

// tripErrors are the keys the backend answers a refused trip with. They are the
// ones the service used before the migration, carried over on spec: a 409 with
// {"message": "already_active_trip"} is confirmed, the rest are not, and an
// unmatched key simply falls through to the generic error.
var tripErrors = map[string]error{
	"already_has_active_trip":     ErrAlreadyHasActiveTrip,
	"already_active_trip":         ErrAlreadyHasActiveTrip,
	"bike_in_repair":              ErrBikeInRepair,
	"bike_on_repair":              ErrBikeInRepair,
	"not_enough_balance":          ErrNotEnoughBalance,
	"trip_interval_limit":         ErrTripIntervalLimit,
	"has_no_active_subscriptions": ErrHasNoActiveSubscriptions,
	"bike_already_in_trip":        ErrBikeAlreadyInTrip,
	"no_bike_found":               ErrNoBikeFound,
	"unable_to_start_trip":        ErrUnableToStartTrip,
	"trip_not_found":              ErrTripNotFound,
}

// convertError maps a backend failure onto the errors the bot explains to the
// rider.
func convertError(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *vaimoo.Error
	if !errors.As(err, &apiErr) {
		return err
	}

	// The key arrives as the whole message on a refused trip, and buried in a
	// wrapped exception string on some other failures.
	for key, mapped := range tripErrors {
		if apiErr.Message == key || strings.Contains(apiErr.Message, key) {
			return mapped
		}
	}

	switch apiErr.Status {
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusServiceUnavailable:
		return ErrServiceUnavailable
	}
	return err
}

// innerStation is a station document as Firestore stores it.
type innerStation struct {
	DockingStationId int64  `json:"DockingStationId"`
	AvailableBikes   int    `json:"AvailableBikes"`
	FreeDocks        int    `json:"FreeDocks"`
	DockLimit        int    `json:"DockLimit"`
	Name             string `json:"Name"`

	Street                   string `json:"Street"`
	StreetBuildingIdentifier string `json:"StreetBuildingIdentifier"`
	City                     string `json:"City"`

	Location struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"Location"`

	IsActive      bool   `json:"IsActive"`
	ServiceStatus string `json:"ServiceStatus"`
}

func (i innerStation) export() Station {
	status := AssetStatusInactive
	if i.IsActive && i.ServiceStatus == "AVAILABLE" {
		status = AssetStatusActive
	}

	description := strings.Join(slices.DeleteFunc(
		[]string{i.StreetBuildingIdentifier, i.Street, i.City},
		func(s string) bool { return strings.TrimSpace(s) == "" },
	), " ")

	return Station{
		Serial: StationSerial(strconv.FormatInt(i.DockingStationId, 10)),
		Status: status,

		Name:        i.Name,
		Description: description,

		Latitude:  i.Location.Latitude,
		Longitude: i.Location.Longitude,

		Docks:     max(i.DockLimit, 0),
		Bikes:     max(i.AvailableBikes, 0),
		FreeDocks: max(i.FreeDocks, 0),
	}
}

// innerBike is a bike document as Firestore stores it. The misspelled
// IsAvaliable is the backend's, not ours.
type innerBike struct {
	VisualId        string `json:"VisualId"`
	CommunicationId string `json:"CommunicationId"`

	BatteryPercentage *float64 `json:"BatteryPercentage"`

	DockingStationId     int64   `json:"DockingStationId"`
	DockingPointVisualId *string `json:"DockingPointVisualId"`

	IsAvaliable bool `json:"IsAvaliable"`
	IsBooked    bool `json:"IsBooked"`

	TripId           *int64  `json:"TripId"`
	TripVehicleState *string `json:"TripVehicleState"`
	TripErrorCode    *int    `json:"TripErrorCode"`
}

func (i innerBike) available() bool {
	return i.IsAvaliable && !i.IsBooked && i.CommunicationId != ""
}

func (i innerBike) export() Bike {
	b := Bike{
		Name:   i.VisualId,
		CommID: i.CommunicationId,
	}

	if i.BatteryPercentage != nil {
		b.Battery = strconv.Itoa(int(*i.BatteryPercentage))
	}

	if i.DockingPointVisualId != nil {
		b.DockNumber, _ = strconv.Atoi(strings.TrimLeft(*i.DockingPointVisualId, "0"))
	}

	return b
}
