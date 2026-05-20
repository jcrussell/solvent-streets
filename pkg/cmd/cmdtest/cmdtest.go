// Package cmdtest provides shared fixtures and helpers for command-package
// tests under pkg/cmd. Keep helpers minimal — only the fields tests actually
// exercise — so they don't drift into a parallel definition of the production
// types.
package cmdtest

import "github.com/jcrussell/solvent-streets/internal/config"

// NewTestCity returns a CityConfig populated with the minimum fields command
// tests rely on. Callers may mutate the returned pointer to set additional
// fields (e.g. Overpass) before handing it to a Factory.
func NewTestCity() *config.CityConfig {
	return &config.CityConfig{Name: "Test City"}
}

// NewTestConfig wraps city in a *config.Config whose Cities slice contains
// a copy of it, matching the production shape where Config owns the slice.
func NewTestConfig(city *config.CityConfig) *config.Config {
	return &config.Config{Cities: []config.CityConfig{*city}}
}
