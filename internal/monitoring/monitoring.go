package monitoring

import (
	"math"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

const (
	StateDisabled    = "disabled"
	StateUnavailable = "unavailable"
	StateIdle        = "idle"
	StateNormal      = "normal"
	StateWarning     = "warning"
	StateTrip        = "trip"
)

type Thresholds struct {
	TemperatureWarningC float64 `json:"temperatureWarningC,omitempty"`
	TemperatureTripC    float64 `json:"temperatureTripC,omitempty"`
	TemperatureResetC   float64 `json:"temperatureResetC,omitempty"`
	SWRWarning          float64 `json:"swrWarning,omitempty"`
	SWRTrip             float64 `json:"swrTrip,omitempty"`
}

type Observations struct {
	MaximumTemperatureC *float64 `json:"maximumTemperatureC,omitempty"`
	TemperatureSensor   string   `json:"temperatureSensor,omitempty"`
	MaximumSWR          *float64 `json:"maximumSwr,omitempty"`
	SWRSource           string   `json:"swrSource,omitempty"`
	TX                  *bool    `json:"tx,omitempty"`
}

type Condition struct {
	Metric           string   `json:"metric"`
	State            string   `json:"state"`
	Value            *float64 `json:"value,omitempty"`
	WarningThreshold float64  `json:"warningThreshold"`
	TripThreshold    float64  `json:"tripThreshold"`
	Evaluated        bool     `json:"evaluated"`
	Reason           string   `json:"reason,omitempty"`
}

type Result struct {
	Enabled      bool         `json:"enabled"`
	Armed        bool         `json:"armed"`
	DryRun       bool         `json:"dryRun"`
	State        string       `json:"state"`
	Reason       string       `json:"reason,omitempty"`
	Thresholds   Thresholds   `json:"thresholds"`
	Observations Observations `json:"observations"`
	Conditions   []Condition  `json:"conditions"`
	ActionsTaken []string     `json:"actionsTaken"`
	Latched      bool         `json:"latched"`
	Action       ActionStatus `json:"action"`
}

func Evaluate(status api.Status, enabled bool, thresholds Thresholds) Result {
	result := Result{
		Enabled:      enabled,
		DryRun:       true,
		State:        StateDisabled,
		Thresholds:   thresholds,
		Observations: observations(status),
		Conditions:   []Condition{},
		ActionsTaken: []string{},
	}
	if !enabled {
		result.Reason = "safety monitoring is disabled"
		return result
	}
	if !status.RecentContact {
		result.State = StateUnavailable
		result.Reason = "amplifier status is not recent"
		return result
	}

	if configured(thresholds.TemperatureWarningC, thresholds.TemperatureTripC) {
		result.Conditions = append(result.Conditions, evaluateTemperature(result.Observations, thresholds))
	}
	if configured(thresholds.SWRWarning, thresholds.SWRTrip) {
		result.Conditions = append(result.Conditions, evaluateSWR(result.Observations, thresholds))
	}
	result.State, result.Reason = summarize(result.Conditions)
	return result
}

func observations(status api.Status) Observations {
	temperature, temperatureSensor := maximumReading(
		namedReading{"upper", status.TemperatureC},
		namedReading{"lower", status.TemperatureLowerC},
		namedReading{"combiner", status.TemperatureCombinerC},
	)
	swr, swrSource := maximumReading(
		namedReading{"atu", status.SWR},
		namedReading{"antenna", status.AntennaSWR},
	)
	return Observations{
		MaximumTemperatureC: temperature,
		TemperatureSensor:   temperatureSensor,
		MaximumSWR:          swr,
		SWRSource:           swrSource,
		TX:                  status.TX,
	}
}

type namedReading struct {
	name  string
	value *float64
}

func maximumReading(readings ...namedReading) (*float64, string) {
	var maximum *float64
	name := ""
	for _, reading := range readings {
		if reading.value == nil || math.IsNaN(*reading.value) || math.IsInf(*reading.value, 0) {
			continue
		}
		if maximum == nil || *reading.value > *maximum {
			value := *reading.value
			maximum = &value
			name = reading.name
		}
	}
	return maximum, name
}

func evaluateTemperature(observations Observations, thresholds Thresholds) Condition {
	condition := Condition{
		Metric:           "temperatureC",
		State:            StateUnavailable,
		Value:            observations.MaximumTemperatureC,
		WarningThreshold: thresholds.TemperatureWarningC,
		TripThreshold:    thresholds.TemperatureTripC,
	}
	if condition.Value == nil {
		condition.Reason = "no temperature reading is available"
		return condition
	}
	condition.Evaluated = true
	condition.State = level(*condition.Value, condition.WarningThreshold, condition.TripThreshold)
	return condition
}

func evaluateSWR(observations Observations, thresholds Thresholds) Condition {
	condition := Condition{
		Metric:           "swr",
		State:            StateIdle,
		Value:            observations.MaximumSWR,
		WarningThreshold: thresholds.SWRWarning,
		TripThreshold:    thresholds.SWRTrip,
	}
	if observations.TX == nil || !*observations.TX {
		condition.Reason = "SWR is evaluated only while transmitting"
		return condition
	}
	if condition.Value == nil {
		condition.State = StateUnavailable
		condition.Reason = "no SWR reading is available during TX"
		return condition
	}
	condition.Evaluated = true
	condition.State = level(*condition.Value, condition.WarningThreshold, condition.TripThreshold)
	return condition
}

func level(value, warning, trip float64) string {
	switch {
	case value >= trip:
		return StateTrip
	case value >= warning:
		return StateWarning
	default:
		return StateNormal
	}
}

func summarize(conditions []Condition) (string, string) {
	if len(conditions) == 0 {
		return StateUnavailable, "no monitoring thresholds are configured"
	}
	hasEvaluated := false
	hasUnavailable := false
	for _, condition := range conditions {
		switch condition.State {
		case StateTrip:
			return StateTrip, "one or more configured trip thresholds are exceeded"
		case StateWarning:
			hasEvaluated = true
		case StateNormal:
			hasEvaluated = true
		case StateUnavailable:
			hasUnavailable = true
		}
	}
	for _, condition := range conditions {
		if condition.State == StateWarning {
			return StateWarning, "one or more warning thresholds are exceeded"
		}
	}
	if hasUnavailable {
		return StateUnavailable, "one or more configured readings are unavailable"
	}
	if hasEvaluated {
		return StateNormal, ""
	}
	return StateIdle, "configured monitoring is waiting for an eligible reading"
}

func configured(warning, trip float64) bool {
	return warning > 0 && trip > warning
}
