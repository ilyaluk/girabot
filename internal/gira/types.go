package gira

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shurcooL/graphql"
)

type (
	Serial string
	Code   string

	StationSerial Serial
	DockSerial    Serial
	BikeSerial    Serial

	UserCode         Code
	StationCode      Code
	DockCode         Code
	BikeCode         Code
	TripCode         Code
	SubscriptionCode Code

	AssetStatus string
	BikeType    string
)

var (
	AssetStatusActive AssetStatus = "active"

	BikeTypeElectric     BikeType = "electric"
	BikeTypeConventional BikeType = "conventional"
)

type ClientInfo struct {
	Code    UserCode
	Name    string
	Balance float64
	Bonus   int

	ActiveSubscriptions []ClientSubscription
}

type ClientSubscription struct {
	Code   SubscriptionCode
	User   UserCode
	Client UserCode

	SubscriptionStatus string
	Active             bool
	ActivationDate     time.Time
	ExpirationDate     time.Time

	Subscription            string
	Cost                    float64
	SubscriptionCode        string
	SubscriptionName        string
	SubscriptionDescription string
}

type Station struct {
	Code   StationCode
	Serial StationSerial
	Status AssetStatus

	Type        string
	Name        string
	Description string

	Latitude  float64
	Longitude float64

	Docks int
	Bikes int
}

func (s Station) Number() string {
	num, _, _ := strings.Cut(s.Name, "-")
	return strings.TrimSpace(num)
}

func (s Station) Location() string {
	if s.Description != "" {
		return s.Description
	}

	_, name, _ := strings.Cut(s.Name, "-")
	return strings.TrimSpace(name)
}

type Dock struct {
	Code   DockCode
	Serial DockSerial
	Status AssetStatus
	Parent StationCode

	Number     int
	LedStatus  string
	LockStatus string

	Bike *Bike
}

func (d Dock) PrettyString() string {
	if d.Bike == nil {
		return fmt.Sprint(d.Number)
	}
	return fmt.Sprintf("[%d] %s", d.Number, d.Bike.PrettyString())
}

type Bike struct {
	Code   BikeCode
	Serial BikeSerial
	Status AssetStatus
	Parent DockCode

	Name    string
	Type    BikeType
	Battery string
}

func (b Bike) PrettyString() string {
	switch b.Type {
	case BikeTypeConventional:
		return fmt.Sprintf("⚙️ %s", b.Name)
	case BikeTypeElectric:
		return fmt.Sprintf("⚡️ %s %s", b.Name, b.PrettyBattery())
	}
	return fmt.Sprintf("❓ %s", b.Name)
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

func (b Bike) TextString() string {
	switch b.Type {
	case BikeTypeConventional:
		return fmt.Sprintf("Bike️ %s", b.Name)
	case BikeTypeElectric:
		return fmt.Sprintf("Electric bike %s, battery %s", b.Name, b.TextBattery())
	}
	return fmt.Sprintf("Unknown bike type %s", b.Name)
}

func (b Bike) TextBattery() string {
	switch b.Battery {
	case "":
		return ""
	default:
		return b.Battery + "%"
	}
}

type Docks []Dock

func (ds Docks) ElectricBikesAvailable() int {
	var res int
	for _, d := range ds {
		if d.Bike != nil && d.Bike.Type == BikeTypeElectric && d.Bike.Status == AssetStatusActive {
			res++
		}
	}
	return res
}

func (ds Docks) ConventionalBikesAvailable() int {
	var res int
	for _, d := range ds {
		if d.Bike != nil && d.Bike.Type == BikeTypeConventional && d.Bike.Status == AssetStatusActive {
			res++
		}
	}
	return res
}

func (ds Docks) FreeDocks() int {
	var res int
	for _, d := range ds {
		if d.Bike == nil && d.Status == AssetStatusActive && d.LedStatus == "green" && d.LockStatus == "unlocked" {
			res++
		}
	}
	return res
}

type StationContent struct {
	Docks []Dock
}

type Trip struct {
	Code       TripCode
	TripStatus string

	User     UserCode
	Client   UserCode
	BikeCode BikeCode
	BikeName string

	StartLocation     StationCode
	EndLocation       StationCode
	StartLocationName string
	EndLocationName   string
	StartDate         time.Time
	EndDate           time.Time
	StartOccupation   float64
	EndOccupation     float64
	EndTripDock       DockCode

	Distance   float64
	Cost       float64
	TotalBonus int
	CostBonus  int

	Rating  int
	Photo   string
	Comment string
}

// PrettyDuration returns the duration of the trip in a human-readable format.
// If the trip is still ongoing, the current time is used as the end time.
func (t Trip) PrettyDuration() string {
	endTs := t.EndDate
	if endTs.IsZero() {
		endTs = time.Now()
	}

	duration := int(endTs.Sub(t.StartDate).Seconds())
	h, m, s := duration/3600, (duration/60)%60, duration%60

	durStr := fmt.Sprintf("%02d:%02d", m, s)
	if h > 0 {
		durStr = fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return durStr
}

type innerClientInfo struct {
	Code    graphql.String
	Name    graphql.String
	Balance graphql.Float
	Bonus   graphql.Int

	// unused, but defined fields
	//FiscalNumber      graphql.String
	//PaypalReference   graphql.String
	//Address           graphql.String
	//PostalCode        graphql.String
	//City              graphql.String
	//Type              graphql.String
	//TransactionIdBond graphql.String
	//EasypayCustomer   graphql.String
	//LisboaVivaSn      graphql.String
	//NifCountry        graphql.String
	//NumberNavegante   graphql.String
	//Description       graphql.String
	//CreationDate      graphql.String
	//CreatedBy         graphql.String
	//UpdateDate        graphql.String
	//UpdatedBy         graphql.String
	//DefaultOrder      graphql.Int
	//Version           graphql.Int
}

func (i innerClientInfo) export() ClientInfo {
	return ClientInfo{
		Code:    UserCode(i.Code),
		Name:    string(i.Name),
		Balance: float64(i.Balance),
		Bonus:   int(i.Bonus),
	}
}

type innerSubscriptionType struct {
	Code        graphql.String
	Name        graphql.String
	Description graphql.String

	//CreationDate graphql.String
	//CreatedBy    graphql.String
	//UpdateDate   graphql.String
	//UpdatedBy    graphql.String
	//DefaultOrder graphql.Int
	//Version      graphql.Int
}

type innerClientSubscription struct {
	Code   graphql.String
	User   graphql.String
	Client graphql.String

	SubscriptionStatus graphql.String
	Active             graphql.Boolean
	ActivationDate     graphql.String
	ExpirationDate     graphql.String

	Subscription graphql.String
	Cost         graphql.Float
	Type         innerSubscriptionType

	//Name               graphql.String
	//Description        graphql.String
	//CreationDate       graphql.String
	//CreatedBy          graphql.String
	//UpdateDate         graphql.String
	//UpdatedBy          graphql.String
	//DefaultOrder       graphql.Int
	//Version            graphql.Int
}

func (i innerClientSubscription) export() ClientSubscription {
	activationDate, _ := time.Parse(time.RFC3339, string(i.ActivationDate))
	expirationDate, _ := time.Parse(time.RFC3339, string(i.ExpirationDate))

	return ClientSubscription{
		Code:   SubscriptionCode(i.Code),
		User:   UserCode(i.User),
		Client: UserCode(i.Client),

		SubscriptionStatus: string(i.SubscriptionStatus),
		Active:             bool(i.Active),
		ActivationDate:     activationDate,
		ExpirationDate:     expirationDate,

		Subscription:            string(i.Subscription),
		Cost:                    float64(i.Cost),
		SubscriptionCode:        string(i.Type.Code),
		SubscriptionName:        string(i.Type.Name),
		SubscriptionDescription: string(i.Type.Description),
	}
}

type innerStation struct {
	Docks        graphql.Int
	Bikes        graphql.Int
	Stype        graphql.String
	SerialNumber graphql.String
	AssetStatus  graphql.String
	Latitude     graphql.Float
	Longitude    graphql.Float
	Code         graphql.String
	Name         graphql.String
	Description  graphql.String

	//AssetType      graphql.String
	//AssetCondition graphql.String
	//Parent         graphql.String
	//Warehouse      graphql.String
	//Zone           graphql.String
	//Location       graphql.String
	//CreationDate   graphql.String
	//CreatedBy      graphql.String
	//UpdateDate     graphql.String
	//UpdatedBy      graphql.String
	//DefaultOrder   graphql.Int
	//Version        graphql.Int
}

func (i innerStation) export() Station {
	return Station{
		Code:   StationCode(i.Code),
		Serial: StationSerial(i.SerialNumber),
		Status: AssetStatus(i.AssetStatus),

		Name:        string(i.Name),
		Description: string(i.Description),
		Type:        string(i.Stype),

		Latitude:  float64(i.Latitude),
		Longitude: float64(i.Longitude),

		Docks: int(i.Docks),
		Bikes: int(i.Bikes),
	}
}

type innerDock struct {
	LedStatus    graphql.String
	LockStatus   graphql.String
	SerialNumber graphql.String
	AssetStatus  graphql.String
	Parent       graphql.String
	Code         graphql.String
	Name         graphql.String

	//AssetType      graphql.String
	//AssetCondition graphql.String
	//Warehouse      graphql.String
	//Zone           graphql.String
	//Location       graphql.String
	//Latitude       graphql.Float
	//Longitude      graphql.Float
	//Description    graphql.String
	//CreationDate   graphql.String
	//CreatedBy      graphql.String
	//UpdateDate     graphql.String
	//UpdatedBy      graphql.String
	//DefaultOrder   graphql.Int
	//Version        graphql.Int
}

func (i innerDock) export() Dock {
	num, _ := strconv.Atoi(string(i.Name))

	return Dock{
		Code:   DockCode(i.Code),
		Serial: DockSerial(i.SerialNumber),
		Status: AssetStatus(i.AssetStatus),
		Parent: StationCode(i.Parent),

		Number:     num,
		LedStatus:  string(i.LedStatus),
		LockStatus: string(i.LockStatus),
	}
}

type innerBike struct {
	Type         graphql.String
	Battery      graphql.String
	SerialNumber graphql.String
	AssetStatus  graphql.String
	Parent       graphql.String
	Code         graphql.String
	Name         graphql.String

	//AssetType      graphql.String
	//AssetCondition graphql.String
	//Warehouse      graphql.String
	//Zone           graphql.String
	//Location       graphql.String
	//Latitude       graphql.Float
	//Longitude      graphql.Float
	//Kms            graphql.String
	//Description    graphql.String
	//CreationDate   graphql.String
	//CreatedBy      graphql.String
	//UpdateDate     graphql.String
	//UpdatedBy      graphql.String
	//DefaultOrder   graphql.Int
	//Version        graphql.Int
}

func (i innerBike) export() Bike {
	b := Bike{
		Code:   BikeCode(i.Code),
		Serial: BikeSerial(i.SerialNumber),
		Status: AssetStatus(i.AssetStatus),
		Parent: DockCode(i.Parent),

		Name:    string(i.Name),
		Type:    BikeType(i.Type),
		Battery: string(i.Battery),
	}

	if b.Type == "" {
		// sometimes the type is not set, so we try to infer it from the name
		switch b.Name[0] {
		case 'E':
			b.Type = BikeTypeElectric
		case 'C':
			b.Type = BikeTypeConventional
		}
	}

	if b.Type == BikeTypeElectric && b.Battery == "" {
		// sometimes the battery is not set
		b.Battery = "?"
	}

	return b
}

type innerTrip struct {
	User            graphql.String
	Asset           graphql.String
	StartDate       graphql.String
	EndDate         graphql.String
	StartLocation   graphql.String
	EndLocation     graphql.String
	Distance        graphql.Float
	Rating          graphql.Int
	Photo           graphql.String
	Cost            graphql.Float
	StartOccupation graphql.Float
	EndOccupation   graphql.Float
	TotalBonus      graphql.Int
	Client          graphql.String
	CostBonus       graphql.Int
	Comment         graphql.String
	EndTripDock     graphql.String
	TripStatus      graphql.String
	Code            graphql.String
	Name            graphql.String

	//CompensationTime graphql.Boolean
	//Description      graphql.String
	//CreationDate     graphql.String
	//CreatedBy        graphql.String
	//UpdateDate       graphql.String
	//UpdatedBy        graphql.String
	//DefaultOrder     graphql.Int
	//Version          graphql.Int
}

func (i innerTrip) export() Trip {
	startTime, _ := time.Parse(time.RFC3339, string(i.StartDate))
	endTime, _ := time.Parse(time.RFC3339, string(i.EndDate))

	return Trip{
		User:            UserCode(i.User),
		BikeCode:        BikeCode(i.Asset),
		StartDate:       startTime,
		EndDate:         endTime,
		StartLocation:   StationCode(i.StartLocation),
		EndLocation:     StationCode(i.EndLocation),
		Distance:        float64(i.Distance),
		Rating:          int(i.Rating),
		Photo:           string(i.Photo),
		Cost:            float64(i.Cost),
		StartOccupation: float64(i.StartOccupation),
		EndOccupation:   float64(i.EndOccupation),
		TotalBonus:      int(i.TotalBonus),
		Client:          UserCode(i.Client),
		CostBonus:       int(i.CostBonus),
		Comment:         string(i.Comment),
		EndTripDock:     DockCode(i.EndTripDock),
		TripStatus:      string(i.TripStatus),
		Code:            TripCode(i.Code),
	}
}

type innerTripDetail struct {
	Code          graphql.String
	StartDate     graphql.String
	EndDate       graphql.String
	Rating        graphql.Int
	BikeName      graphql.String
	StartLocation graphql.String
	EndLocation   graphql.String
	Bonus         graphql.Int
	UsedPoints    graphql.Int
	Cost          graphql.Float
	BikeType      graphql.String
}

func (i innerTripDetail) export() Trip {
	startTime, _ := time.Parse(time.RFC3339, string(i.StartDate))
	endTime, _ := time.Parse(time.RFC3339, string(i.EndDate))

	return Trip{
		Code:      TripCode(i.Code),
		StartDate: startTime,
		EndDate:   endTime,
		Rating:    int(i.Rating),

		// TODO: convert to asset IDs
		BikeName:          string(i.BikeName),
		StartLocationName: string(i.StartLocation),
		EndLocationName:   string(i.EndLocation),

		TotalBonus: int(i.Bonus),
		CostBonus:  int(i.UsedPoints),
		Cost:       float64(i.Cost),
	}
}
