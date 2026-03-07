package geo

import (
	"math"
	"testing"
)

func TestUTMZoneDetection(t *testing.T) {
	tests := []struct {
		name string
		lon  float64
		lat  float64
		zone int
	}{
		{"Livermore CA", -121.76, 37.68, 10},
		{"New York", -74.0, 40.71, 18},
		{"Austin TX", -97.74, 30.27, 14},
		{"Seattle WA", -122.33, 47.61, 10},
		{"Miami FL", -80.19, 25.76, 17},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewUTMProjector(tt.lon, tt.lat)
			if p.Zone != tt.zone {
				t.Errorf("zone = %d, want %d", p.Zone, tt.zone)
			}
			if !p.Northern {
				t.Error("expected Northern = true for US city")
			}
		})
	}
}

func TestUTMRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		lon  float64
		lat  float64
	}{
		{"Livermore CA", -121.76, 37.68},
		{"New York", -74.0, 40.71},
		{"Austin TX", -97.74, 30.27},
		{"Anchorage AK", -149.9, 61.22},
		{"Honolulu HI", -157.86, 21.31},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewUTMProjector(tt.lon, tt.lat)
			x, y, err := p.ToProjected(tt.lon, tt.lat)
			if err != nil {
				t.Fatal(err)
			}
			lon2, lat2, err := p.FromProjected(x, y)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(lon2-tt.lon) > 1e-7 {
				t.Errorf("lon round-trip: got %f, want %f", lon2, tt.lon)
			}
			if math.Abs(lat2-tt.lat) > 1e-7 {
				t.Errorf("lat round-trip: got %f, want %f", lat2, tt.lat)
			}
		})
	}
}

func TestUTMDistanceMeters(t *testing.T) {
	// Two points ~1.77 km apart east-west near Livermore
	p := NewUTMProjector(-121.76, 37.68)
	x1, y1, _ := p.ToProjected(-121.76, 37.68)
	x2, y2, _ := p.ToProjected(-121.74, 37.68)

	dx := x2 - x1
	dy := y2 - y1
	dist := math.Sqrt(dx*dx + dy*dy)

	// 0.02 degrees longitude at 37.68N ~ 1763 meters
	if dist < 1700 || dist > 1850 {
		t.Errorf("expected ~1763m, got %f", dist)
	}
}
