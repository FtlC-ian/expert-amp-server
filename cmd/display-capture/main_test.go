package main

import (
	"strings"
	"testing"
)

func TestRunRejectsNonPositiveRequestTimeout(t *testing.T) {
	for _, value := range []string{"0", "-1s"} {
		t.Run(value, func(t *testing.T) {
			err := run([]string{"-request-timeout", value})
			if err == nil || !strings.Contains(err.Error(), "request timeout must be positive") {
				t.Fatalf("run error = %v, want positive-timeout validation", err)
			}
		})
	}
}
