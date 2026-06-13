package iostreams

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

func (cs *ColorScheme) Red(t string) string {
	return cs.wrap("\033[31m", t)
}

func (cs *ColorScheme) wrap(code, t string) string {
	if !cs.enabled {
		return t
	}
	return code + t + "\033[0m"
}
