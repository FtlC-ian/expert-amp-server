package api

import "strings"

type ButtonAction struct {
	Name string `json:"name"`
}

func (a ButtonAction) Normalized() ButtonAction {
	a.Name = strings.ToLower(strings.TrimSpace(a.Name))
	return a
}

type ActionResult struct {
	Name      string `json:"name"`
	Queued    bool   `json:"queued"`
	Sent      bool   `json:"sent"`
	Transport string `json:"transport,omitempty"`
	FrameHex  string `json:"frameHex,omitempty"`
}

type Telemetry struct {
	ModelName                  string   `json:"modelName,omitempty"`
	OperatingState             string   `json:"operatingState,omitempty"`
	Mode                       string   `json:"mode,omitempty"`
	TX                         *bool    `json:"tx,omitempty"`
	Band                       string   `json:"band,omitempty"`
	Input                      string   `json:"input,omitempty"`
	Antenna                    string   `json:"antenna,omitempty"`
	AntennaBank                string   `json:"antennaBank,omitempty"`
	CATInterface               string   `json:"catInterface,omitempty"`
	CATMode                    string   `json:"catMode,omitempty"`
	OutputLevel                string   `json:"outputLevel,omitempty"`
	SWR                        *float64 `json:"swr,omitempty"`
	SWRDisplay                 string   `json:"swrDisplay,omitempty"`
	AntennaSWR                 *float64 `json:"antennaSwr,omitempty"`
	AntennaSWRDisplay          string   `json:"antennaSwrDisplay,omitempty"`
	PASupplyVoltage            *float64 `json:"paSupplyVoltage,omitempty"`
	PASupplyVoltageDisplay     string   `json:"paSupplyVoltageDisplay,omitempty"`
	PACurrent                  *float64 `json:"paCurrent,omitempty"`
	PACurrentDisplay           string   `json:"paCurrentDisplay,omitempty"`
	TemperatureC               *float64 `json:"temperatureC,omitempty"`
	TemperatureDisplay         string   `json:"temperatureDisplay,omitempty"`
	TemperatureLowerC          *float64 `json:"temperatureLowerC,omitempty"`
	TemperatureLowerDisplay    string   `json:"temperatureLowerDisplay,omitempty"`
	TemperatureCombinerC       *float64 `json:"temperatureCombinerC,omitempty"`
	TemperatureCombinerDisplay string   `json:"temperatureCombinerDisplay,omitempty"`
	Frequency                  string   `json:"frequency,omitempty"`
	PowerWatts                 *float64 `json:"powerWatts,omitempty"`
	Source                     string   `json:"source,omitempty"`
	Confidence                 string   `json:"confidence,omitempty"`
	Provenance                 string   `json:"provenance,omitempty"`
	Notes                      []string `json:"notes,omitempty"`
}

type Status struct {
	Telemetry
	RecentContact bool     `json:"recentContact,omitempty"`
	LastContactAt string   `json:"lastContactAt,omitempty"`
	BandCode      string   `json:"bandCode,omitempty"`
	BandText      string   `json:"bandText,omitempty"`
	RXAntenna     string   `json:"rxAntenna,omitempty"`
	WarningCode   string   `json:"warningCode,omitempty"`
	AlarmCode     string   `json:"alarmCode,omitempty"`
	ATUStatusCode string   `json:"atuStatusCode,omitempty"`
	WarningsText  []string `json:"warningsText,omitempty"`
	AlarmsText    []string `json:"alarmsText,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	ActiveAlarms  []string `json:"activeAlarms,omitempty"`
}

type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}
