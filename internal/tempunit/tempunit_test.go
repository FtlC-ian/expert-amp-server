package tempunit

import (
	"math"
	"testing"
)

func TestNormalize(t *testing.T) {
	for input, want := range map[string]string{
		"C":   Celsius,
		"c":   Celsius,
		" F ": Fahrenheit,
	} {
		got, ok := Normalize(input)
		if !ok || got != want {
			t.Fatalf("Normalize(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	if _, ok := Normalize("kelvin"); ok {
		t.Fatal("unsupported unit was accepted")
	}
}

func TestToCelsius(t *testing.T) {
	if got := ToCelsius(25, Celsius); got != 25 {
		t.Fatalf("25 C = %v C", got)
	}
	if got := ToCelsius(86, Fahrenheit); math.Abs(got-30) > 0.0001 {
		t.Fatalf("86 F = %v C, want 30", got)
	}
}

func TestFormat(t *testing.T) {
	if got := Format(86, Fahrenheit); got != "86 F" {
		t.Fatalf("Format = %q, want 86 F", got)
	}
}
