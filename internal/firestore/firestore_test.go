package firestore

import (
	"encoding/json"
	"testing"
)

// A synthetic document covering every value shape the Gira collections use.
const sampleDocument = `{
	"BikeId": {"integerValue": "24784"},
	"VisualId": {"stringValue": "E2032"},
	"IsBooked": {"booleanValue": false},
	"BatteryPercentage": {"doubleValue": 82},
	"NfcCardId": {"nullValue": null},
	"TripId": {"integerValue": "1971005"},
	"Location": {"geoPointValue": {"latitude": 38.71, "longitude": -9.19}},
	"AvailableCategories": {"arrayValue": {}},
	"Categories": {"arrayValue": {"values": [{"stringValue": "E-Bike"}, {"integerValue": "7"}]}},
	"Nested": {"mapValue": {"fields": {"Code": {"stringValue": "LSB"}}}},
	"UpdatedAt": {"timestampValue": "2026-09-16T00:35:03.297295Z"}
}`

func decodeSample(t *testing.T) map[string]any {
	t.Helper()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(sampleDocument), &fields); err != nil {
		t.Fatalf("unmarshaling the sample: %v", err)
	}

	decoded, err := decodeFields(fields)
	if err != nil {
		t.Fatalf("decoding fields: %v", err)
	}
	return decoded
}

func TestDecodeFields(t *testing.T) {
	decoded := decodeSample(t)

	if got := decoded["VisualId"]; got != "E2032" {
		t.Errorf("VisualId = %#v", got)
	}
	if got := decoded["IsBooked"]; got != false {
		t.Errorf("IsBooked = %#v", got)
	}
	if got := decoded["BatteryPercentage"]; got != float64(82) {
		t.Errorf("BatteryPercentage = %#v", got)
	}
	if got := decoded["NfcCardId"]; got != nil {
		t.Errorf("NfcCardId = %#v", got)
	}
	if got, ok := decoded["Location"].(map[string]any); !ok || got["latitude"] != 38.71 {
		t.Errorf("Location = %#v", decoded["Location"])
	}
	if got, ok := decoded["Categories"].([]any); !ok || len(got) != 2 || got[0] != "E-Bike" {
		t.Errorf("Categories = %#v", decoded["Categories"])
	}
	if got, ok := decoded["AvailableCategories"].([]any); !ok || len(got) != 0 {
		t.Errorf("AvailableCategories = %#v", decoded["AvailableCategories"])
	}
	if got, ok := decoded["Nested"].(map[string]any); !ok || got["Code"] != "LSB" {
		t.Errorf("Nested = %#v", decoded["Nested"])
	}
}

// Firestore sends 64 bit integers as strings; they have to survive the trip
// into a numeric struct field.
func TestDecodeFieldsIntegersStayNumbers(t *testing.T) {
	plain, err := json.Marshal(decodeSample(t))
	if err != nil {
		t.Fatalf("re-encoding: %v", err)
	}

	var bike struct {
		BikeId            int64   `json:"BikeId"`
		TripId            *int64  `json:"TripId"`
		BatteryPercentage float64 `json:"BatteryPercentage"`
		VisualId          string  `json:"VisualId"`
	}
	if err := json.Unmarshal(plain, &bike); err != nil {
		t.Fatalf("decoding into a struct: %v", err)
	}

	if bike.BikeId != 24784 {
		t.Errorf("BikeId = %d", bike.BikeId)
	}
	if bike.TripId == nil || *bike.TripId != 1971005 {
		t.Errorf("TripId = %v", bike.TripId)
	}
	if bike.BatteryPercentage != 82 {
		t.Errorf("BatteryPercentage = %v", bike.BatteryPercentage)
	}
	if bike.VisualId != "E2032" {
		t.Errorf("VisualId = %q", bike.VisualId)
	}
}

func TestQueryBody(t *testing.T) {
	body, err := queryBody("bikes", []Filter{
		Equal("Tenant", "P1/EML/EML/"),
		Equal("DockingStationId", int64(4551)),
	})
	if err != nil {
		t.Fatalf("building the query: %v", err)
	}

	const want = `{"structuredQuery":{"from":[{"collectionId":"bikes"}],` +
		`"where":{"compositeFilter":{"filters":[` +
		`{"fieldFilter":{"field":{"fieldPath":"Tenant"},"op":"EQUAL","value":{"stringValue":"P1/EML/EML/"}}},` +
		`{"fieldFilter":{"field":{"fieldPath":"DockingStationId"},"op":"EQUAL","value":{"integerValue":"4551"}}}` +
		`],"op":"AND"}}}}`
	if string(body) != want {
		t.Errorf("got  %s\nwant %s", body, want)
	}
}

func TestQueryBodySingleFilter(t *testing.T) {
	body, err := queryBody("docking-stations", []Filter{Equal("Tenant", "P1/EML/EML/")})
	if err != nil {
		t.Fatalf("building the query: %v", err)
	}

	const want = `{"structuredQuery":{"from":[{"collectionId":"docking-stations"}],` +
		`"where":{"fieldFilter":{"field":{"fieldPath":"Tenant"},"op":"EQUAL","value":{"stringValue":"P1/EML/EML/"}}}}}`
	if string(body) != want {
		t.Errorf("got  %s\nwant %s", body, want)
	}
}
