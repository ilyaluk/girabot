package gira

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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

func (s Station) MapTitle() string {
	return fmt.Sprintf("Station %s: %s", s.Number(), s.Location())
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

func (d Dock) ButtonString(isMax bool) string {
	if d.Bike == nil {
		return fmt.Sprint(d.Number)
	}
	if isMax {
		return fmt.Sprintf("<%d> %s", d.Number, d.Bike.PrettyString())
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

	// set only if returned from GetStationDocks
	DockNumber int
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
	res := fmt.Sprintf("Dock %d; ", b.DockNumber)

	switch b.Type {
	case BikeTypeConventional:
		res += fmt.Sprintf("Bike️ %s", b.Name)
	case BikeTypeElectric:
		res += fmt.Sprintf("Electric bike %s, battery %s", b.Name, b.TextBattery())
	default:
		res += fmt.Sprintf("Unknown bike type %s", b.Name)
	}

	return res
}

// CallbackData returns the callback data for the bike.
// It contains enough data to show info about bike.
func (b Bike) CallbackData() string {
	return strings.Join([]string{
		string(b.Serial),
		b.Name,
		b.Battery,
		fmt.Sprint(b.DockNumber),
	}, "|")
}

// BikeFromCallbackData parses the callback data and returns the bike.
func BikeFromCallbackData(data string) (b Bike, err error) {
	parts := strings.Split(data, "|")
	if len(parts) != 4 {
		return Bike{}, fmt.Errorf("invalid callback data: %s", data)
	}

	b = Bike{
		Serial:  BikeSerial(parts[0]),
		Name:    parts[1],
		Battery: parts[2],
	}
	b.DockNumber, _ = strconv.Atoi(parts[3])

	switch b.Name[0] {
	case 'E':
		b.Type = BikeTypeElectric
	case 'C':
		b.Type = BikeTypeConventional
	}

	return b, nil
}

func (b Bike) TextBattery() string {
	switch b.Battery {
	case "":
		return ""
	default:
		return b.Battery + "%"
	}
}

func (b Bike) Number() int {
	if len(b.Name) < 2 {
		return 0
	}
	num, _ := strconv.Atoi(b.Name[1:])
	return num
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

type innerClientInfo struct {
	Code    string
	Name    string
	Balance float64
	Bonus   int32

	// unused, but defined fields
	//FiscalNumber      string
	//PaypalReference   string
	//Address           string
	//PostalCode        string
	//City              string
	//Type              string
	//TransactionIdBond string
	//EasypayCustomer   string
	//LisboaVivaSn      string
	//NifCountry        string
	//NumberNavegante   string
	//Description       string
	//CreationDate      string
	//CreatedBy         string
	//UpdateDate        string
	//UpdatedBy         string
	//DefaultOrder      int32
	//Version           int32
}

func (i innerClientInfo) export() ClientInfo {
	return ClientInfo{
		Code:    UserCode(i.Code),
		Name:    i.Name,
		Balance: i.Balance,
		Bonus:   int(i.Bonus),
	}
}

type innerSubscriptionType struct {
	Code        string
	Name        string
	Description string

	//CreationDate string
	//CreatedBy    string
	//UpdateDate   string
	//UpdatedBy    string
	//DefaultOrder int32
	//Version      int32
}

type innerClientSubscription struct {
	Code   string
	User   string
	Client string

	SubscriptionStatus string
	Active             bool
	ActivationDate     string
	ExpirationDate     string

	Subscription string
	Cost         float64
	Type         innerSubscriptionType

	//Name               string
	//Description        string
	//CreationDate       string
	//CreatedBy          string
	//UpdateDate         string
	//UpdatedBy          string
	//DefaultOrder       int32
	//Version            int32
}

func (i innerClientSubscription) export() ClientSubscription {
	activationDate, _ := time.Parse(time.RFC3339, i.ActivationDate)
	expirationDate, _ := time.Parse(time.RFC3339, i.ExpirationDate)

	return ClientSubscription{
		Code:   SubscriptionCode(i.Code),
		User:   UserCode(i.User),
		Client: UserCode(i.Client),

		SubscriptionStatus: i.SubscriptionStatus,
		Active:             i.Active,
		ActivationDate:     activationDate,
		ExpirationDate:     expirationDate,

		Subscription:            i.Subscription,
		Cost:                    i.Cost,
		SubscriptionCode:        i.Type.Code,
		SubscriptionName:        i.Type.Name,
		SubscriptionDescription: i.Type.Description,
	}
}

type innerStation struct {
	Docks        int32
	Bikes        int32
	Stype        string
	SerialNumber string
	AssetStatus  string
	Latitude     float64
	Longitude    float64
	Code         string
	Name         string
	Description  string

	//AssetType      string
	//AssetCondition string
	//Parent         string
	//Warehouse      string
	//Zone           string
	//Location       string
	//CreationDate   string
	//CreatedBy      string
	//UpdateDate     string
	//UpdatedBy      string
	//DefaultOrder   int32
	//Version        int32
}

func (i innerStation) export() Station {
	return Station{
		Code:   StationCode(i.Code),
		Serial: StationSerial(i.SerialNumber),
		Status: AssetStatus(i.AssetStatus),

		Name:        i.Name,
		Description: i.Description,
		Type:        i.Stype,

		Latitude:  i.Latitude,
		Longitude: i.Longitude,

		Docks: int(i.Docks),
		Bikes: int(i.Bikes),
	}
}

type innerDock struct {
	LedStatus    string
	LockStatus   string
	SerialNumber string
	AssetStatus  string
	Parent       string
	Code         string
	Name         string

	//AssetType      string
	//AssetCondition string
	//Warehouse      string
	//Zone           string
	//Location       string
	//Latitude       float64
	//Longitude      float64
	//Description    string
	//CreationDate   string
	//CreatedBy      string
	//UpdateDate     string
	//UpdatedBy      string
	//DefaultOrder   int32
	//Version        int32
}

func (i innerDock) export() Dock {
	num, _ := strconv.Atoi(i.Name)

	return Dock{
		Code:   DockCode(i.Code),
		Serial: DockSerial(i.SerialNumber),
		Status: AssetStatus(i.AssetStatus),
		Parent: StationCode(i.Parent),

		Number:     num,
		LedStatus:  i.LedStatus,
		LockStatus: i.LockStatus,
	}
}

type innerBike struct {
	Type         string
	Battery      string
	SerialNumber string
	AssetStatus  string
	Parent       string
	Code         string
	Name         string

	//AssetType      string
	//AssetCondition string
	//Warehouse      string
	//Zone           string
	//Location       string
	//Latitude       float64
	//Longitude      float64
	//Kms            string
	//Description    string
	//CreationDate   string
	//CreatedBy      string
	//UpdateDate     string
	//UpdatedBy      string
	//DefaultOrder   int32
	//Version        int32
}

func (i innerBike) export() Bike {
	b := Bike{
		Code:   BikeCode(i.Code),
		Serial: BikeSerial(i.SerialNumber),
		Status: AssetStatus(i.AssetStatus),
		Parent: DockCode(i.Parent),

		Name:    i.Name,
		Type:    BikeType(i.Type),
		Battery: i.Battery,
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
	User            string
	Asset           string
	StartDate       string
	EndDate         string
	StartLocation   string
	EndLocation     string
	Distance        float64
	Rating          int32
	Photo           string
	Cost            float64
	StartOccupation float64
	EndOccupation   float64
	TotalBonus      int32
	Client          string
	CostBonus       int32
	Comment         string
	EndTripDock     string
	TripStatus      string
	Code            string
	Name            string

	//CompensationTime bool
	//Description      string
	//CreationDate     string
	//CreatedBy        string
	//UpdateDate       string
	//UpdatedBy        string
	//DefaultOrder     int32
	//Version          int32
}

func (i innerTrip) export() Trip {
	startTime, _ := time.Parse(time.RFC3339, i.StartDate)
	endTime, _ := time.Parse(time.RFC3339, i.EndDate)

	return Trip{
		User:            UserCode(i.User),
		BikeCode:        BikeCode(i.Asset),
		StartDate:       startTime,
		EndDate:         endTime,
		StartLocation:   StationCode(i.StartLocation),
		EndLocation:     StationCode(i.EndLocation),
		Distance:        i.Distance,
		Rating:          int(i.Rating),
		Photo:           i.Photo,
		Cost:            i.Cost,
		StartOccupation: i.StartOccupation,
		EndOccupation:   i.EndOccupation,
		TotalBonus:      int(i.TotalBonus),
		Client:          UserCode(i.Client),
		CostBonus:       int(i.CostBonus),
		Comment:         i.Comment,
		EndTripDock:     DockCode(i.EndTripDock),
		TripStatus:      i.TripStatus,
		Code:            TripCode(i.Code),
	}
}

type innerTripDetail struct {
	Code          string
	StartDate     string
	EndDate       string
	Rating        int32
	BikeName      string
	StartLocation string
	EndLocation   string
	Bonus         int32
	UsedPoints    int32
	Cost          float64
	BikeType      string
}

func (i innerTripDetail) export() Trip {
	startTime, _ := time.Parse(time.RFC3339, i.StartDate)
	endTime, _ := time.Parse(time.RFC3339, i.EndDate)

	return Trip{
		Code:      TripCode(i.Code),
		StartDate: startTime,
		EndDate:   endTime,
		Rating:    int(i.Rating),

		// TODO: convert to asset IDs
		BikeName:          i.BikeName,
		StartLocationName: i.StartLocation,
		EndLocationName:   i.EndLocation,

		TotalBonus: int(i.Bonus),
		CostBonus:  int(i.UsedPoints),
		Cost:       i.Cost,
	}
}
