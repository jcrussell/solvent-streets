package iostreams

import "fmt"

// ColorScheme provides ANSI color formatting when enabled.
type ColorScheme struct {
	enabled bool
}

func NewColorScheme(enabled bool) *ColorScheme {
	return &ColorScheme{enabled: enabled}
}

func (cs *ColorScheme) Bold(t string) string {
	return cs.wrap("\033[1m", t)
}

func (cs *ColorScheme) Green(t string) string {
	return cs.wrap("\033[32m", t)
}

func (cs *ColorScheme) Yellow(t string) string {
	return cs.wrap("\033[33m", t)
}

func (cs *ColorScheme) Red(t string) string {
	return cs.wrap("\033[31m", t)
}

func (cs *ColorScheme) Gray(t string) string {
	return cs.wrap("\033[90m", t)
}

func (cs *ColorScheme) Cyan(t string) string {
	return cs.wrap("\033[36m", t)
}

func (cs *ColorScheme) Boldf(format string, a ...any) string {
	return cs.Bold(fmt.Sprintf(format, a...))
}

func (cs *ColorScheme) Greenf(format string, a ...any) string {
	return cs.Green(fmt.Sprintf(format, a...))
}

func (cs *ColorScheme) Redf(format string, a ...any) string {
	return cs.Red(fmt.Sprintf(format, a...))
}

func (cs *ColorScheme) wrap(code, t string) string {
	if !cs.enabled {
		return t
	}
	return code + t + "\033[0m"
}
