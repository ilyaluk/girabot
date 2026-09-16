package gira

import "testing"

func TestBikeCallbackDataRoundTrip(t *testing.T) {
	bike := Bike{
		Name:       "E2032",
		CommID:     "132E07360C",
		Battery:    "82",
		DockNumber: 13,
	}

	data := bike.CallbackData()
	if len(data) > MaxCallbackDataLen {
		t.Errorf("callback data is %d bytes, budget is %d: %q", len(data), MaxCallbackDataLen, data)
	}

	got, err := BikeFromCallbackData(data)
	if err != nil {
		t.Fatalf("parsing %q: %v", data, err)
	}
	if got != bike {
		t.Errorf("got %+v, want %+v", got, bike)
	}
}

func TestBikeFromCallbackDataRejectsGarbage(t *testing.T) {
	for _, data := range []string{"", "a|b", "|E2032|82|13", "a|b|c|d|e"} {
		if _, err := BikeFromCallbackData(data); err == nil {
			t.Errorf("parsing %q should have failed", data)
		}
	}
}

func TestStationNameParts(t *testing.T) {
	station := Station{
		Name:        "253 - Avenida da Universidade Técnica / FMV",
		Description: "Avenida da Universidade Tecnica Lisboa",
	}

	if got := station.Number(); got != "253" {
		t.Errorf("Number() = %q", got)
	}
	if want := "Avenida da Universidade Técnica / FMV"; station.Location() != want {
		t.Errorf("Location() = %q, want %q", station.Location(), want)
	}

	// A name without the usual "<number> - <location>" shape falls back to the
	// postal address.
	unnamed := Station{Name: "1234", Description: "Somewhere"}
	if got := unnamed.Location(); got != "Somewhere" {
		t.Errorf("Location() = %q", got)
	}
}

func TestBikeExport(t *testing.T) {
	battery := 82.0
	dock := "01"
	doc := innerBike{
		VisualId:             "E2032",
		CommunicationId:      "132E07360C",
		DockingStationId:     4551,
		DockingPointVisualId: &dock,
		BatteryPercentage:    &battery,
	}

	want := Bike{
		Name:       "E2032",
		CommID:     "132E07360C",
		Battery:    "82",
		DockNumber: 1,
	}
	if got := doc.export(); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// An undocked bike with an unknown charge still has to render.
	bare := innerBike{VisualId: "E7", DockingStationId: 4551}.export()
	if bare.DockNumber != 0 || bare.Battery != "" {
		t.Errorf("got %+v", bare)
	}
	if got := bare.TextString(); got != "Electric bike E7, battery unknown" {
		t.Errorf("TextString() = %q", got)
	}
}

func TestStationExport(t *testing.T) {
	doc := innerStation{
		DockingStationId: 4545,
		Name:             "253 - Avenida",
		Street:           "Avenida",
		City:             "Lisboa",
		DockLimit:        32,
		AvailableBikes:   3,
		FreeDocks:        5,
		IsActive:         true,
		ServiceStatus:    "AVAILABLE",
	}
	station := doc.export()

	if station.Serial != "4545" || station.Status != AssetStatusActive {
		t.Errorf("got %+v", station)
	}
	if station.Docks != 32 || station.Bikes != 3 || station.FreeDocks != 5 {
		t.Errorf("counts are %d/%d/%d", station.Docks, station.Bikes, station.FreeDocks)
	}
	if station.Description != "Avenida Lisboa" {
		t.Errorf("Description = %q", station.Description)
	}

	doc.ServiceStatus = "UNAVAILABLE_BY_OPERATOR"
	if got := doc.export().Status; got != AssetStatusInactive {
		t.Errorf("Status = %q", got)
	}
}
