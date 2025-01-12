package gira

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/hasura/go-graphql-client"

	"github.com/ilyaluk/girabot/internal/giraauth"
)

var (
	ErrNoActiveTrip = fmt.Errorf("gira: no active trip")

	ErrAlreadyHasActiveTrip     = fmt.Errorf("gira: already has active trip")
	ErrBikeAlreadyReserved      = fmt.Errorf("gira: bike already reserved")
	ErrNotEnoughBalance         = fmt.Errorf("gira: not enough balance")
	ErrTripIntervalLimit        = fmt.Errorf("gira: trip interval limit")
	ErrHasNoActiveSubscriptions = fmt.Errorf("gira: has no active subscriptions")
	ErrNoServiceStatusFound     = fmt.Errorf("gira: no service status found")
	ErrBikeAlreadyInTrip        = fmt.Errorf("gira: bike already in trip")
	ErrTMLCommunication         = fmt.Errorf("gira: TML communication error")
	ErrServiceUnavailable       = fmt.Errorf("gira: service unavailable")
	ErrForbidden                = fmt.Errorf("gira: forbidden")
)

type Client struct {
	c *graphql.Client
}

var (
	stationCacheMu sync.Mutex
	stationCache   = map[StationSerial]Station{}
)

func New(httpc *http.Client) *Client {
	t := &retryableTransport{
		inner: httpc.Transport,
	}
	httpc.Transport = t

	return &Client{
		c: graphql.NewClient("https://egira-proxy-arqetk5clq-ew.a.run.app/api/graphql", httpc),
	}
}

func (c *Client) GetClientInfo(ctx context.Context) (ClientInfo, error) {
	var query struct {
		Client              []innerClientInfo         `graphql:"client"`
		ActiveSubscriptions []innerClientSubscription `graphql:"activeSubscriptions"`
	}

	if err := c.c.Query(ctx, &query, nil); err != nil {
		return ClientInfo{}, wrapError(err)
	}

	if len(query.Client) != 1 {
		return ClientInfo{}, fmt.Errorf("gira: expected 1 client info, got %d", len(query.Client))
	}

	res := query.Client[0].export()
	for _, s := range query.ActiveSubscriptions {
		res.ActiveSubscriptions = append(res.ActiveSubscriptions, s.export())
	}

	return res, nil
}

func (c *Client) GetStations(ctx context.Context) ([]Station, error) {
	res, err := c.getStationsNoCache(ctx)
	if err != nil {
		return nil, err
	}

	stationCacheMu.Lock()
	defer stationCacheMu.Unlock()
	fillStationCache(res)

	return res, nil
}

func (c *Client) getStationsNoCache(ctx context.Context) ([]Station, error) {
	var query struct {
		GetStations []innerStation
	}
	if err := c.c.Query(ctx, &query, nil); err != nil {
		return nil, wrapError(err)
	}

	res := make([]Station, len(query.GetStations))
	for i, station := range query.GetStations {
		res[i] = station.export()
	}
	return res, nil
}

// fillStationCache fills the station cache with the given stations.
// It assumes the caller has locked stationCacheMu.
func fillStationCache(res []Station) {
	stationCache = make(map[StationSerial]Station, len(res))
	for _, station := range res {
		stationCache[station.Serial] = station
	}
}

// GetStationCached returns a station from the cache if it exists, otherwise it calls GetStation.
// This is useful to avoid calling GetStations multiple times if up-to-date data like free dock count is not required.
func (c *Client) GetStationCached(ctx context.Context, serial StationSerial) (Station, error) {
	stationCacheMu.Lock()
	defer stationCacheMu.Unlock()

	if len(stationCache) == 0 {
		stations, err := c.getStationsNoCache(ctx)
		if err != nil {
			return Station{}, err
		}
		fillStationCache(stations)
	}

	station, ok := stationCache[serial]
	if !ok {
		return Station{}, fmt.Errorf("gira: station %s not found in cache", serial)
	}
	return station, nil
}

func (c *Client) GetStationDocks(ctx context.Context, id StationSerial) (Docks, error) {
	var query struct {
		GetDocks []innerDock `graphql:"getDocks(input: $input)"`
		GetBikes []innerBike `graphql:"getBikes(input: $input)"`
	}

	err := c.c.Query(ctx, &query, map[string]any{
		"input": string(id),
	})
	if err != nil {
		return nil, wrapError(err)
	}

	res := make(Docks, 0, len(query.GetDocks))
	for _, dock := range query.GetDocks {
		res = append(res, dock.export())
	}

	for _, bike := range query.GetBikes {
		b := bike.export()
		found := false
		for i, dock := range res {
			if b.Parent == dock.Code {
				found = true
				b.DockNumber = dock.Number
				res[i].Bike = &b
				break
			}
		}

		if !found {
			// generally should be unreachable
			log.Printf("gira: bike without dock in station: %+v, %+v", b, query)
			res = append(res, Dock{
				Code: b.Parent,
				Bike: &b,
			})
		}
	}

	slices.SortFunc(res, func(i, j Dock) int {
		return cmp.Compare(i.Number, j.Number)
	})

	return res, nil
}

func (c *Client) ReserveBike(ctx context.Context, id BikeSerial) (bool, error) {
	var mutation struct {
		ReserveBike bool `graphql:"reserveBike(input: $input)"`
	}

	if err := c.c.Mutate(ctx, &mutation, map[string]any{
		"input": string(id),
	}); err != nil {
		return false, wrapError(err)
	}

	return mutation.ReserveBike, nil
}

func (c *Client) CancelBikeReserve(ctx context.Context) (bool, error) {
	var mutation struct {
		CancelBikeReserve bool
	}

	if err := c.c.Mutate(ctx, &mutation, nil); err != nil {
		return false, wrapError(err)
	}

	return mutation.CancelBikeReserve, nil
}

func (c *Client) StartTrip(ctx context.Context) (bool, error) {
	var mutation struct {
		StartTrip bool
	}

	if err := c.c.Mutate(ctx, &mutation, nil); err != nil {
		return false, wrapError(err)
	}

	return mutation.StartTrip, nil
}

func (c *Client) GetActiveTrip(ctx context.Context) (Trip, error) {
	var query struct {
		ActiveTrip *innerTrip
	}

	if err := c.c.Query(ctx, &query, nil); err != nil {
		return Trip{}, wrapError(err)
	}

	if query.ActiveTrip == nil {
		return Trip{}, ErrNoActiveTrip
	}
	return query.ActiveTrip.export(), nil
}

func (c *Client) GetTrip(ctx context.Context, code TripCode) (Trip, error) {
	var query struct {
		Trip []innerTrip `graphql:"getTrip(input: $input)"`
	}

	if err := c.c.Query(ctx, &query, map[string]any{
		"input": string(code),
	}); err != nil {
		return Trip{}, wrapError(err)
	}

	if len(query.Trip) == 0 {
		return Trip{}, fmt.Errorf("gira: trip %s not found", code)
	}
	if len(query.Trip) > 1 {
		return Trip{}, fmt.Errorf("gira: expected 1 trip, got %d", len(query.Trip))
	}
	return query.Trip[0].export(), nil
}

type pageInput struct {
	PageNum  int32 `json:"_pageNum"`
	PageSize int32 `json:"_pageSize"`
}

func (pageInput) GetGraphQLType() string {
	return "PageInput"
}

func (c *Client) GetTripHistory(ctx context.Context, page, pageSize int) ([]Trip, error) {
	var query struct {
		TripHistory []innerTripDetail `graphql:"tripHistory(pageInput: $pageInput)"`
	}

	if err := c.c.Query(ctx, &query, map[string]any{
		"pageInput": pageInput{
			PageNum:  int32(page),
			PageSize: int32(pageSize),
		},
	}); err != nil {
		return nil, wrapError(err)
	}

	res := make([]Trip, len(query.TripHistory))
	for i, trip := range query.TripHistory {
		res[i] = trip.export()
	}
	return res, nil

}

func (c *Client) GetUnratedTrips(ctx context.Context, page, pageSize int) ([]Trip, error) {
	var query struct {
		UnratedTrips []innerTrip `graphql:"unratedTrips(pageInput: $pageInput)"`
	}

	if err := c.c.Query(ctx, &query, map[string]any{
		"pageInput": pageInput{
			PageNum:  int32(page),
			PageSize: int32(pageSize),
		},
	}); err != nil {
		return nil, wrapError(err)
	}

	res := make([]Trip, len(query.UnratedTrips))
	for i, trip := range query.UnratedTrips {
		res[i] = trip.export()
	}

	return res, nil
}

type TripRating struct {
	Rating  int
	Comment string
}

func (c *Client) RateTrip(ctx context.Context, code TripCode, rating TripRating) (bool, error) {
	//goland:noinspection ALL
	type RateTrip_In struct {
		Code        string `graphql:"code" json:"code"`
		Rating      int    `graphql:"rating" json:"rating"`
		Description string `graphql:"description" json:"description"`
		//Attachment  Attachment
	}

	var mutation struct {
		RateTrip bool `graphql:"rateTrip(in: $in)"`
	}

	if err := c.c.Mutate(ctx, &mutation, map[string]any{
		"in": RateTrip_In{
			Code:        string(code),
			Rating:      rating.Rating,
			Description: rating.Comment,
		},
	}); err != nil {
		return false, wrapError(err)
	}

	return mutation.RateTrip, nil
}

func (c *Client) PayTripWithPoints(ctx context.Context, id TripCode) (int, error) {
	var mutation struct {
		TripPay int `graphql:"tripPayWithPoints(input: $input)"`
	}

	if err := c.c.Mutate(ctx, &mutation, map[string]any{
		"input": string(id),
	}); err != nil {
		return 0, wrapError(err)
	}

	return mutation.TripPay, nil
}

func (c *Client) PayTripWithMoney(ctx context.Context, id TripCode) (int, error) {
	var mutation struct {
		TripPay int `graphql:"tripPayWithNoPoints(input: $input)"`
	}

	if err := c.c.Mutate(ctx, &mutation, map[string]any{
		"input": string(id),
	}); err != nil {
		return 0, wrapError(err)
	}

	return mutation.TripPay, nil
}

func wrapError(err error) error {
	var errs graphql.Errors
	if errors.As(err, &errs) && len(errs) == 1 {
		msg := errs[0].Message

		switch {
		case strings.Contains(msg, giraauth.ErrInternalServer.Error()): // sigh, graphql lib breaks errors
			return giraauth.ErrInternalServer
		case strings.Contains(msg, giraauth.ErrInvalidRefreshToken.Error()):
			return giraauth.ErrInvalidRefreshToken
		case strings.Contains(msg, "record not found"):
			// this happens when token is deleted from DB for some reason (maybe refresh failed)
			// as graphql lib breaks any errors to strings, we have to check like this...
			return giraauth.ErrInvalidRefreshToken
		}

		if err := convertTripError(msg); err != nil {
			return err
		}
	}
	return err
}

func convertTripError(msg string) error {
	switch {
	case strings.Contains(msg, "already_has_active_trip"):
		return ErrAlreadyHasActiveTrip
	case strings.Contains(msg, "bike_already_reserved"):
		return ErrBikeAlreadyReserved
	case strings.Contains(msg, "not_enough_balance"):
		return ErrNotEnoughBalance
	case strings.Contains(msg, "trip_interval_limit"):
		return ErrTripIntervalLimit
	case strings.Contains(msg, "has_no_active_subscriptions"):
		return ErrHasNoActiveSubscriptions
	case strings.Contains(msg, "no_service_status_found"):
		return ErrNoServiceStatusFound
	case strings.Contains(msg, "bike_already_in_trip"):
		return ErrBikeAlreadyInTrip
	case strings.Contains(msg, "navigator_error_tml_comunication"): // yes, it's spelled like this
		return ErrTMLCommunication
	case strings.Contains(msg, "Serviço indisponível"), strings.Contains(msg, `Servi\u00E7o indispon\u00EDvel`):
		return ErrServiceUnavailable
	case strings.Contains(msg, "403 Forbidden"):
		return ErrForbidden
	case strings.Contains(msg, "401 Unauthorized"):
		return ErrForbidden
	}
	return nil
}
