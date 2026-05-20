package cmdtest

import "testing"

func TestNewTestCity_HasName(t *testing.T) {
	c := NewTestCity()
	if c.Name != "Test City" {
		t.Errorf("Name = %q, want %q", c.Name, "Test City")
	}
}

// Mutating one returned city must not bleed into a later one — each call
// returns a fresh pointer so tests can customize Overpass etc. in isolation.
func TestNewTestCity_FreshPerCall(t *testing.T) {
	a := NewTestCity()
	a.Overpass = true
	b := NewTestCity()
	if b.Overpass {
		t.Error("NewTestCity returned a shared instance; mutation leaked across calls")
	}
}

func TestNewTestConfig_CopiesCity(t *testing.T) {
	c := NewTestCity()
	cfg := NewTestConfig(c)
	if len(cfg.Cities) != 1 {
		t.Fatalf("Cities len = %d, want 1", len(cfg.Cities))
	}
	if cfg.Cities[0].Name != c.Name {
		t.Errorf("Cities[0].Name = %q, want %q", cfg.Cities[0].Name, c.Name)
	}

	// Mutating the source pointer after construction must not affect the
	// already-built config — NewTestConfig copies the value into the slice.
	c.Name = "Mutated"
	if cfg.Cities[0].Name == "Mutated" {
		t.Error("NewTestConfig stored a reference; expected a value copy")
	}
}
