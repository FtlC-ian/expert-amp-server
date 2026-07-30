package tempunit

import (
	"fmt"
	"strings"
)

const (
	Celsius    = "C"
	Fahrenheit = "F"
)

func Normalize(value string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case Celsius:
		return Celsius, true
	case Fahrenheit:
		return Fahrenheit, true
	default:
		return "", false
	}
}

func ToCelsius(value float64, unit string) float64 {
	if normalized, ok := Normalize(unit); ok && normalized == Fahrenheit {
		return (value - 32) * 5 / 9
	}
	return value
}

func Format(value float64, unit string) string {
	normalized, ok := Normalize(unit)
	if !ok {
		normalized = Celsius
	}
	return fmt.Sprintf("%.0f %s", value, normalized)
}
