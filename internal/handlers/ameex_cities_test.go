package handlers

import "testing"

// TRT custom patch #63: Ameex returns cities nested under "api" as an id-keyed map,
// not a top-level array. Guard the parser against regressing to array-only.
func TestParseAmeexCities_NestedMap(t *testing.T) {
	raw := []byte(`{"login":"success","api":{"type":"success","msg":"","cities":{` +
		`"2":{"id":2,"name":"Meknes"},` +
		`"1":{"id":1,"name":"Marrakech"},` +
		`"17":{"id":17,"name":"Nador"}}}}`)

	cities := parseAmeexCities(raw)
	if len(cities) != 3 {
		t.Fatalf("expected 3 cities, got %d (%+v)", len(cities), cities)
	}
	// Sorted ascending by id regardless of map order.
	if cities[0].ID != 1 || cities[0].Name != "Marrakech" {
		t.Errorf("expected first city Marrakech(1), got %+v", cities[0])
	}
	if cities[2].ID != 17 || cities[2].Name != "Nador" {
		t.Errorf("expected last city Nador(17), got %+v", cities[2])
	}
}

// A bare array (the shape the old parser assumed) must still work.
func TestParseAmeexCities_BareArray(t *testing.T) {
	raw := []byte(`[{"id":5,"name":"Casablanca"},{"id":3,"name":"Rabat"}]`)
	cities := parseAmeexCities(raw)
	if len(cities) != 2 {
		t.Fatalf("expected 2 cities, got %d", len(cities))
	}
	if cities[0].Name != "Rabat" || cities[1].Name != "Casablanca" {
		t.Errorf("expected id-sorted [Rabat, Casablanca], got %+v", cities)
	}
}
