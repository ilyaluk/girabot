package gira

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type (
	// StationSerial identifies a station. It is the VAIMOO docking station id.
	StationSerial string
	// TripCode identifies a trip. It is the VAIMOO trip id.
	TripCode string

	AssetStatus string
)

const (
	AssetStatusActive   AssetStatus = "active"
	AssetStatusInactive AssetStatus = "inactive"
)

// ClientInfo is the rider's account.
type ClientInfo struct {
	Name  string
	Email string

	// Balance is the wallet balance in euro.
	Balance float64

	ActiveSubscriptions []ClientSubscription
}

// ClientSubscription is one pass the rider holds.
type ClientSubscription struct {
	Name        string
	Description string

	Active         bool
	ExpirationDate time.Time
}

// Station is a docking station.
type Station struct {
	Serial StationSerial
	Status AssetStatus

	// Name is "<number> - <location>", e.g. "253 - Avenida da Universidade".
	Name string
	// Description is the postal address, which is often less readable than the
	// location part of Name.
	Description string

	Latitude  float64
	Longitude float64

	// Docks is the number of docking points, Bikes the number of bikes the
	// backend counts as available, FreeDocks the number of empty docks.
	Docks     int
	Bikes     int
	FreeDocks int
}

// Number returns the station number riders and signage use.
func (s Station) Number() string {
	num, _, _ := strings.Cut(s.Name, "-")
	return strings.TrimSpace(num)
}

// Location returns the human readable part of the station name.
func (s Station) Location() string {
	_, name, ok := strings.Cut(s.Name, "-")
	if !ok || strings.TrimSpace(name) == "" {
		return s.Description
	}
	return strings.TrimSpace(name)
}

func (s Station) MapTitle() string {
	return fmt.Sprintf("Station %s: %s", s.Number(), s.Location())
}

// Bike is a bike sitting in a dock. The Gira fleet is electric throughout.
type Bike struct {
	// Name is the plate printed on the frame, e.g. "E2032".
	Name string
	// CommID is the communication id, which is what unlocking a bike needs.
	CommID string

	Battery string

	// DockNumber is the dock the bike sits in, zero when unknown.
	DockNumber int
}

func (b Bike) PrettyString() string {
	return fmt.Sprintf("⚡️ %s %s", b.Name, b.PrettyBattery())
}

func (b Bike) PrettyBattery() string {
	switch b.Battery {
	case "100":
		return "💯"
	case "":
		return ""
	default:
		return b.Battery + "%"
	}
}

// ButtonString renders the bike for a station keyboard. isMax marks the bike
// with the highest plate number, which tends to be the newest one.
func (b Bike) ButtonString(isMax bool) string {
	if b.DockNumber == 0 {
		return b.PrettyString()
	}
	if isMax {
		return fmt.Sprintf("{%d} %s", b.DockNumber, b.PrettyString())
	}
	return fmt.Sprintf("[%d] %s", b.DockNumber, b.PrettyString())
}

func (b Bike) TextString() string {
	var res string
	if b.DockNumber != 0 {
		res = fmt.Sprintf("Dock %d; ", b.DockNumber)
	}
	return res + fmt.Sprintf("Electric bike %s, battery %s", b.Name, b.TextBattery())
}

// MaxCallbackDataLen is what a bike's callback data has to fit into. Telegram
// allows 64 bytes for the whole payload, of which the button's own key and its
// separator take the rest.
const MaxCallbackDataLen = 64 - len("unlock_bike") - len("\f|")

// CallbackData returns the callback data for the bike. It carries enough to
// show the bike and to unlock it without another lookup.
func (b Bike) CallbackData() string {
	return strings.Join([]string{
		b.CommID,
		b.Name,
		b.Battery,
		fmt.Sprint(b.DockNumber),
	}, "|")
}

// BikeFromCallbackData parses the callback data and returns the bike.
func BikeFromCallbackData(data string) (b Bike, err error) {
	parts := strings.Split(data, "|")
	if len(parts) != 4 || len(data) > MaxCallbackDataLen {
		return Bike{}, fmt.Errorf("invalid callback data: %s", data)
	}

	b = Bike{
		CommID:  parts[0],
		Name:    parts[1],
		Battery: parts[2],
	}
	// Without both of these the bike can neither be shown nor unlocked.
	if b.Name == "" || b.CommID == "" {
		return Bike{}, fmt.Errorf("invalid callback data: %s", data)
	}
	b.DockNumber, _ = strconv.Atoi(parts[3])

	return b, nil
}

func (b Bike) TextBattery() string {
	if b.Battery == "" {
		return "unknown"
	}
	return b.Battery + "%"
}

// Number returns the numeric part of the bike plate.
func (b Bike) Number() int {
	if len(b.Name) < 2 {
		return 0
	}
	num, _ := strconv.Atoi(b.Name[1:])
	return num
}

// Bikes is a list of bikes available at one station.
type Bikes []Bike

// Trip is a finished trip.
type Trip struct {
	Code TripCode

	BikeName string

	StartDate time.Time
	EndDate   time.Time

	StartLocationName string
	EndLocationName   string
	// EndStation is the station the trip ended at, zero when unknown. Rating a
	// trip needs it.
	EndStation int64

	// Distance is the ridden distance in meters.
	Distance float64
	// Cost is what the trip cost in euro, already settled against the wallet.
	Cost float64
}

// TripRating is a rating the rider gives a finished trip.
type TripRating struct {
	Rating  int
	Comment string
}

// ActiveTrip is a trip in progress.
type ActiveTrip struct {
	Code      TripCode
	BikeName  string
	StartDate time.Time
	// BikeState is the lock state the bike last reported, e.g. RUNNING.
	BikeState string
}

// TripUpdate is one observation of the rider's trip.
type TripUpdate struct {
	Code     TripCode
	Bike     string
	Finished bool

	StartDate time.Time
	EndDate   time.Time

	Cost     float64
	Distance float64

	// ErrorCode is non-zero when the trip could not be started.
	ErrorCode int
}

// PrettyDuration returns the duration of the trip in a human-readable format.
// If the trip is still ongoing, the current time is used as the end time.
func (t TripUpdate) PrettyDuration() string {
	endTs := t.EndDate
	if endTs.IsZero() {
		endTs = time.Now()
	}

	// A start the backend never gave would otherwise count from the year 1.
	var duration int
	if !t.StartDate.IsZero() {
		duration = int(endTs.Sub(t.StartDate).Seconds())
	}
	if duration < 0 {
		duration = 0
	}
	h, m, s := duration/3600, (duration/60)%60, duration%60

	durStr := fmt.Sprintf("%02d:%02d", m, s)
	if h > 0 {
		durStr = fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return durStr
}
