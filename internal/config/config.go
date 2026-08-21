package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/FtlC-ian/expert-amp-server/internal/tempunit"
)

const (
	DefaultPollIntervalMs        = 125
	DefaultFanHighTemperatureC   = 50
	DefaultFanNormalTemperatureC = 42
	MinimumFanHysteresisC        = 5
	FanDisplayProfileFirstSeries = "expert-1.3k-fa-first-series-v1"
)

type PollingMode string

const (
	PollingModeOff     PollingMode = "off"
	PollingModeBoth    PollingMode = "both"
	PollingModeStatus  PollingMode = "status"
	PollingModeDisplay PollingMode = "display"
)

type Settings struct {
	SerialPort            string `json:"serialPort"`
	ListenAddress         string `json:"listenAddress"`
	PollingMode           string `json:"pollingMode"`
	PollIntervalMs        int    `json:"pollIntervalMs"`
	DisplayPollingEnabled bool   `json:"displayPollingEnabled"`
	StatusPollingEnabled  bool   `json:"statusPollingEnabled"`
	// AmplifierTemperatureUnit is the unit selected in the amplifier's SET
	// menu for unitless protocol status values. Policy thresholds remain Celsius.
	AmplifierTemperatureUnit string `json:"amplifierTemperatureUnit"`

	// Cosmetic station labels for the built-in web UI. These do not change
	// protocol/status identity or button behavior.
	PanelModelLabel string            `json:"panelModelLabel,omitempty"`
	InputLabels     map[string]string `json:"inputLabels,omitempty"`
	AntennaLabels   map[string]string `json:"antennaLabels,omitempty"`

	// Safety monitoring is observational by default.
	// OvertemperatureStandbyArmed separately opts in to one guarded OPERATE
	// command that requests STANDBY.
	SafetyMonitoringEnabled     bool    `json:"safetyMonitoringEnabled,omitempty"`
	OvertemperatureStandbyArmed bool    `json:"overtemperatureStandbyArmed,omitempty"`
	TemperatureWarningC         float64 `json:"temperatureWarningC,omitempty"`
	TemperatureTripC            float64 `json:"temperatureTripC,omitempty"`
	TemperatureResetC           float64 `json:"temperatureResetC,omitempty"`
	SWRWarning                  float64 `json:"swrWarning,omitempty"`
	SWRTrip                     float64 `json:"swrTrip,omitempty"`

	// Automatic fan policy is disabled by default. When enabled, it requests
	// high-cooling fan mode at the upper threshold and Normal below the lower
	// threshold, providing hysteresis between the two values.
	AutomaticFanPolicyEnabled bool    `json:"automaticFanPolicyEnabled"`
	FanHighTemperatureC       float64 `json:"fanHighTemperatureC"`
	FanNormalTemperatureC     float64 `json:"fanNormalTemperatureC"`
	FanDisplayProfile         string  `json:"fanDisplayProfile,omitempty"`
	VerifyFanModeOnStartup    bool    `json:"verifyFanModeOnStartup,omitempty"`
	FanBoostDurationMinutes   int     `json:"fanBoostDurationMinutes,omitempty"`

	// Menu debug/reporting is an advanced, explicitly enabled workflow. Only
	// this enable flag is persisted; session tokens and in-progress discovery
	// state remain in memory.
	MenuDebugEnabled bool `json:"menuDebugEnabled"`

	// Serial connection parameters for live ingest.
	SerialBaudRate           int  `json:"serialBaudRate,omitempty"`
	SerialReadTimeoutMs      int  `json:"serialReadTimeoutMs,omitempty"`
	StatusPollCommandEnabled bool `json:"statusPollCommandEnabled,omitempty"`
	StatusPollIntervalMs     int  `json:"statusPollIntervalMs,omitempty"`
	SerialAssertDTR          bool `json:"serialAssertDTR"`
	SerialAssertRTS          bool `json:"serialAssertRTS"`
}

type Snapshot struct {
	Settings   Settings `json:"settings"`
	NeedsSetup bool     `json:"needsSetup"`
	Path       string   `json:"path"`
}

type Manager struct {
	path           string
	mu             sync.RWMutex
	cur            Settings
	fanPolicyState FanPolicyRuntimeState
}

// FanPolicyRuntimeState is server-owned state persisted beside the editable
// settings in the same JSON file. It is intentionally not part of Settings or
// the settings API: clients control the override through the fan-policy action
// API, and the last-verified receipt is written only from verified LCD flows.
type FanPolicyRuntimeState struct {
	ManualOverride                string `json:"manualOverride,omitempty"`
	ManualOverrideDurationMinutes int    `json:"manualOverrideDurationMinutes,omitempty"`
	ManualOverrideUntil           string `json:"manualOverrideUntil,omitempty"`
	LastVerifiedPolicy            string `json:"lastVerifiedPolicy,omitempty"`
	LastVerifiedAt                string `json:"lastVerifiedAt,omitempty"`
	LastVerifiedSource            string `json:"lastVerifiedSource,omitempty"`
	LastVerifiedModel             string `json:"lastVerifiedModel,omitempty"`
}

type rawSettings struct {
	SerialPort                   *string           `json:"serialPort"`
	ListenAddress                *string           `json:"listenAddress"`
	PollingMode                  *string           `json:"pollingMode"`
	PollIntervalMs               *int              `json:"pollIntervalMs"`
	DisplayPollingEnabled        *bool             `json:"displayPollingEnabled"`
	StatusPollingEnabled         *bool             `json:"statusPollingEnabled"`
	AmplifierTemperatureUnit     *string           `json:"amplifierTemperatureUnit"`
	PanelModelLabel              *string           `json:"panelModelLabel,omitempty"`
	InputLabels                  map[string]string `json:"inputLabels,omitempty"`
	AntennaLabels                map[string]string `json:"antennaLabels,omitempty"`
	SafetyMonitoringEnabled      *bool             `json:"safetyMonitoringEnabled,omitempty"`
	OvertemperatureStandbyArmed  *bool             `json:"overtemperatureStandbyArmed,omitempty"`
	OvertemperatureShutdownArmed *bool             `json:"overtemperatureShutdownArmed,omitempty"` // legacy power-off flag; intentionally not migrated
	TemperatureWarningC          *float64          `json:"temperatureWarningC,omitempty"`
	TemperatureTripC             *float64          `json:"temperatureTripC,omitempty"`
	TemperatureResetC            *float64          `json:"temperatureResetC,omitempty"`
	SWRWarning                   *float64          `json:"swrWarning,omitempty"`
	SWRTrip                      *float64          `json:"swrTrip,omitempty"`
	AutomaticFanPolicyEnabled    *bool             `json:"automaticFanPolicyEnabled"`
	FanHighTemperatureC          *float64          `json:"fanHighTemperatureC"`
	FanNormalTemperatureC        *float64          `json:"fanNormalTemperatureC"`
	FanDisplayProfile            *string           `json:"fanDisplayProfile,omitempty"`
	VerifyFanModeOnStartup       *bool             `json:"verifyFanModeOnStartup,omitempty"`
	FanBoostDurationMinutes      *int              `json:"fanBoostDurationMinutes,omitempty"`
	MenuDebugEnabled             *bool             `json:"menuDebugEnabled,omitempty"`

	SerialBaudRate           *int  `json:"serialBaudRate,omitempty"`
	SerialReadTimeoutMs      *int  `json:"serialReadTimeoutMs,omitempty"`
	StatusPollCommandEnabled *bool `json:"statusPollCommandEnabled,omitempty"`
	StatusPollIntervalMs     *int  `json:"statusPollIntervalMs,omitempty"`
	SerialPollEnabled        *bool `json:"serialPollEnabled,omitempty"`
	SerialPollIntervalMs     *int  `json:"serialPollIntervalMs,omitempty"`
	SerialAssertDTR          *bool `json:"serialAssertDTR,omitempty"`
	SerialAssertRTS          *bool `json:"serialAssertRTS,omitempty"`
}

func DefaultSettings(listenAddress string) Settings {
	if listenAddress == "" {
		listenAddress = ":8088"
	}
	return Settings{
		ListenAddress:            listenAddress,
		PollingMode:              string(PollingModeBoth),
		PollIntervalMs:           DefaultPollIntervalMs,
		DisplayPollingEnabled:    true,
		StatusPollingEnabled:     true,
		AmplifierTemperatureUnit: tempunit.Celsius,

		AutomaticFanPolicyEnabled: false,
		FanHighTemperatureC:       DefaultFanHighTemperatureC,
		FanNormalTemperatureC:     DefaultFanNormalTemperatureC,

		// SPE Expert serial defaults.
		SerialBaudRate:           115200,
		SerialReadTimeoutMs:      250,
		StatusPollCommandEnabled: true,
		StatusPollIntervalMs:     DefaultPollIntervalMs,
		SerialAssertDTR:          true,
		SerialAssertRTS:          true,
	}
}

func NewManager(path, listenAddress string) (*Manager, error) {
	if path == "" {
		return nil, errors.New("config path is required")
	}
	m := &Manager{path: path}
	if err := m.LoadOrCreate(listenAddress); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) LoadOrCreate(listenAddress string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	defaults := DefaultSettings(listenAddress)
	data, err := os.ReadFile(m.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		m.cur = defaults
		return writeFile(m.path, m.cur, m.fanPolicyState)
	}

	var raw rawSettings
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var persisted struct {
		FanPolicyState FanPolicyRuntimeState `json:"fanPolicyState"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		return err
	}
	normalized := raw.normalize(defaults)
	normalized = normalizeDisabledFanPolicyThresholds(normalized, defaults)
	if err := validateAmplifierTemperatureUnit(normalized.AmplifierTemperatureUnit); err != nil {
		return err
	}
	if err := validateSafetySettings(normalized); err != nil {
		return fmt.Errorf("invalid safety monitoring settings: %w", err)
	}
	if err := validateAutomaticFanPolicySettings(normalized); err != nil {
		return fmt.Errorf("invalid automatic fan policy settings: %w", err)
	}
	m.cur = normalized
	m.fanPolicyState = normalizeFanPolicyRuntimeState(persisted.FanPolicyState)
	return writeFile(m.path, m.cur, m.fanPolicyState)
}

func normalizeDisabledFanPolicyThresholds(settings, defaults Settings) Settings {
	if settings.AutomaticFanPolicyEnabled {
		return settings
	}
	high := settings.FanHighTemperatureC
	normal := settings.FanNormalTemperatureC
	if math.IsNaN(high) || math.IsInf(high, 0) ||
		math.IsNaN(normal) || math.IsInf(normal, 0) ||
		high <= 0 || normal <= 0 || normal >= high ||
		high-normal < MinimumFanHysteresisC {
		settings.FanHighTemperatureC = defaults.FanHighTemperatureC
		settings.FanNormalTemperatureC = defaults.FanNormalTemperatureC
	}
	return settings
}

func (m *Manager) Get() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Snapshot{
		Settings:   m.cur,
		NeedsSetup: m.cur.SerialPort == "",
		Path:       m.path,
	}
}

func (m *Manager) Update(next Settings) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	normalized, err := validatedSettings(next, DefaultSettings(m.cur.ListenAddress))
	if err != nil {
		return Snapshot{}, err
	}
	if err := writeFile(m.path, normalized, m.fanPolicyState); err != nil {
		return Snapshot{}, err
	}
	m.cur = normalized
	return Snapshot{Settings: m.cur, NeedsSetup: m.cur.SerialPort == "", Path: m.path}, nil
}

func (m *Manager) FanPolicyState() FanPolicyRuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fanPolicyState
}

func (m *Manager) UpdateFanPolicyState(next FanPolicyRuntimeState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	normalized := normalizeFanPolicyRuntimeState(next)
	if err := writeFile(m.path, m.cur, normalized); err != nil {
		return err
	}
	m.fanPolicyState = normalized
	return nil
}

func (r rawSettings) normalize(defaults Settings) Settings {
	out := defaults
	if r.SerialPort != nil {
		out.SerialPort = *r.SerialPort
	}
	if r.ListenAddress != nil && *r.ListenAddress != "" {
		out.ListenAddress = *r.ListenAddress
	}
	modeProvided := r.PollingMode != nil && strings.TrimSpace(*r.PollingMode) != ""
	if modeProvided {
		out.PollingMode = normalizePollingMode(*r.PollingMode)
	}
	if r.PollIntervalMs != nil && *r.PollIntervalMs > 0 {
		out.PollIntervalMs = *r.PollIntervalMs
	}
	if r.DisplayPollingEnabled != nil {
		out.DisplayPollingEnabled = *r.DisplayPollingEnabled
	}
	if r.StatusPollingEnabled != nil {
		out.StatusPollingEnabled = *r.StatusPollingEnabled
	}
	if r.AmplifierTemperatureUnit != nil {
		out.AmplifierTemperatureUnit = strings.ToUpper(strings.TrimSpace(*r.AmplifierTemperatureUnit))
	}
	if r.PanelModelLabel != nil {
		out.PanelModelLabel = normalizeLabel(*r.PanelModelLabel)
	}
	out.InputLabels = normalizeLabelMap(r.InputLabels)
	out.AntennaLabels = normalizeLabelMap(r.AntennaLabels)
	if r.SafetyMonitoringEnabled != nil {
		out.SafetyMonitoringEnabled = *r.SafetyMonitoringEnabled
	}
	if r.OvertemperatureStandbyArmed != nil {
		out.OvertemperatureStandbyArmed = *r.OvertemperatureStandbyArmed
	}
	if r.TemperatureWarningC != nil {
		out.TemperatureWarningC = *r.TemperatureWarningC
	}
	if r.TemperatureTripC != nil {
		out.TemperatureTripC = *r.TemperatureTripC
	}
	if r.TemperatureResetC != nil {
		out.TemperatureResetC = *r.TemperatureResetC
	}
	if r.SWRWarning != nil {
		out.SWRWarning = *r.SWRWarning
	}
	if r.SWRTrip != nil {
		out.SWRTrip = *r.SWRTrip
	}
	if r.AutomaticFanPolicyEnabled != nil {
		out.AutomaticFanPolicyEnabled = *r.AutomaticFanPolicyEnabled
	}
	if r.FanHighTemperatureC != nil && *r.FanHighTemperatureC != 0 {
		out.FanHighTemperatureC = *r.FanHighTemperatureC
	}
	if r.FanNormalTemperatureC != nil && *r.FanNormalTemperatureC != 0 {
		out.FanNormalTemperatureC = *r.FanNormalTemperatureC
	}
	if r.FanDisplayProfile != nil {
		out.FanDisplayProfile = strings.TrimSpace(*r.FanDisplayProfile)
	}
	if r.VerifyFanModeOnStartup != nil {
		out.VerifyFanModeOnStartup = *r.VerifyFanModeOnStartup
	}
	if r.FanBoostDurationMinutes != nil {
		out.FanBoostDurationMinutes = *r.FanBoostDurationMinutes
	}
	if r.MenuDebugEnabled != nil {
		out.MenuDebugEnabled = *r.MenuDebugEnabled
	}
	if r.SerialBaudRate != nil && *r.SerialBaudRate > 0 {
		out.SerialBaudRate = *r.SerialBaudRate
	}
	if r.SerialReadTimeoutMs != nil && *r.SerialReadTimeoutMs > 0 {
		out.SerialReadTimeoutMs = *r.SerialReadTimeoutMs
	}
	if r.StatusPollCommandEnabled != nil {
		out.StatusPollCommandEnabled = *r.StatusPollCommandEnabled
	} else if r.SerialPollEnabled != nil {
		out.StatusPollCommandEnabled = *r.SerialPollEnabled
	}
	if r.StatusPollIntervalMs != nil && *r.StatusPollIntervalMs > 0 {
		out.StatusPollIntervalMs = *r.StatusPollIntervalMs
	} else if r.SerialPollIntervalMs != nil && *r.SerialPollIntervalMs > 0 {
		out.StatusPollIntervalMs = *r.SerialPollIntervalMs
	}
	if !modeProvided {
		out.PollingMode = string(derivePollingMode(out.DisplayPollingEnabled, out.StatusPollingEnabled, out.StatusPollCommandEnabled))
		if (out.PollingMode == string(PollingModeBoth) || out.PollingMode == string(PollingModeStatus)) && r.PollIntervalMs == nil {
			if r.StatusPollIntervalMs != nil && *r.StatusPollIntervalMs > 0 {
				out.PollIntervalMs = *r.StatusPollIntervalMs
			} else if r.SerialPollIntervalMs != nil && *r.SerialPollIntervalMs > 0 {
				out.PollIntervalMs = *r.SerialPollIntervalMs
			}
		}
	}
	out = syncLegacyPollingFields(out)
	if r.SerialAssertDTR != nil {
		out.SerialAssertDTR = *r.SerialAssertDTR
	}
	if r.SerialAssertRTS != nil {
		out.SerialAssertRTS = *r.SerialAssertRTS
	}
	return syncLegacyPollingFields(out)
}

func normalizeSettings(in, defaults Settings) Settings {
	out := defaults
	out.SerialPort = strings.TrimSpace(in.SerialPort)
	if in.ListenAddress != "" {
		out.ListenAddress = strings.TrimSpace(in.ListenAddress)
	}
	if strings.TrimSpace(in.PollingMode) != "" {
		out.PollingMode = normalizePollingMode(in.PollingMode)
	} else {
		out.PollingMode = string(derivePollingMode(in.DisplayPollingEnabled, in.StatusPollingEnabled, in.StatusPollCommandEnabled))
	}
	if in.PollIntervalMs > 0 {
		out.PollIntervalMs = in.PollIntervalMs
	}
	out.PanelModelLabel = normalizeLabel(in.PanelModelLabel)
	if in.AmplifierTemperatureUnit != "" {
		out.AmplifierTemperatureUnit = strings.ToUpper(strings.TrimSpace(in.AmplifierTemperatureUnit))
	}
	out.InputLabels = normalizeLabelMap(in.InputLabels)
	out.AntennaLabels = normalizeLabelMap(in.AntennaLabels)
	out.SafetyMonitoringEnabled = in.SafetyMonitoringEnabled
	out.OvertemperatureStandbyArmed = in.OvertemperatureStandbyArmed
	out.TemperatureWarningC = in.TemperatureWarningC
	out.TemperatureTripC = in.TemperatureTripC
	out.TemperatureResetC = in.TemperatureResetC
	out.SWRWarning = in.SWRWarning
	out.SWRTrip = in.SWRTrip
	out.AutomaticFanPolicyEnabled = in.AutomaticFanPolicyEnabled
	if in.FanHighTemperatureC != 0 {
		out.FanHighTemperatureC = in.FanHighTemperatureC
	}
	if in.FanNormalTemperatureC != 0 {
		out.FanNormalTemperatureC = in.FanNormalTemperatureC
	}
	out.FanDisplayProfile = strings.TrimSpace(in.FanDisplayProfile)
	out.VerifyFanModeOnStartup = in.VerifyFanModeOnStartup
	out.FanBoostDurationMinutes = in.FanBoostDurationMinutes
	out.MenuDebugEnabled = in.MenuDebugEnabled
	if in.SerialBaudRate > 0 {
		out.SerialBaudRate = in.SerialBaudRate
	}
	if in.SerialReadTimeoutMs > 0 {
		out.SerialReadTimeoutMs = in.SerialReadTimeoutMs
	}
	out.StatusPollCommandEnabled = in.StatusPollCommandEnabled
	if in.StatusPollIntervalMs > 0 {
		out.StatusPollIntervalMs = in.StatusPollIntervalMs
	}
	out.SerialAssertDTR = in.SerialAssertDTR
	out.SerialAssertRTS = in.SerialAssertRTS
	return syncLegacyPollingFields(out)
}

func normalizePollingMode(mode string) string {
	switch PollingMode(strings.ToLower(strings.TrimSpace(mode))) {
	case PollingModeOff:
		return string(PollingModeOff)
	case PollingModeStatus:
		return string(PollingModeStatus)
	case PollingModeDisplay:
		return string(PollingModeDisplay)
	default:
		return string(PollingModeBoth)
	}
}

func derivePollingMode(display, status, statusCommand bool) PollingMode {
	if display && status && statusCommand {
		return PollingModeBoth
	}
	if display && !status {
		return PollingModeDisplay
	}
	if !display && status && statusCommand {
		return PollingModeStatus
	}
	return PollingModeOff
}

func syncLegacyPollingFields(in Settings) Settings {
	switch PollingMode(normalizePollingMode(in.PollingMode)) {
	case PollingModeBoth:
		in.PollingMode = string(PollingModeBoth)
		in.DisplayPollingEnabled = true
		in.StatusPollingEnabled = true
		in.StatusPollCommandEnabled = true
	case PollingModeStatus:
		in.PollingMode = string(PollingModeStatus)
		in.DisplayPollingEnabled = false
		in.StatusPollingEnabled = true
		in.StatusPollCommandEnabled = true
	case PollingModeDisplay:
		in.PollingMode = string(PollingModeDisplay)
		in.DisplayPollingEnabled = true
		in.StatusPollingEnabled = false
		in.StatusPollCommandEnabled = false
	default:
		in.PollingMode = string(PollingModeOff)
		in.DisplayPollingEnabled = false
		in.StatusPollingEnabled = false
		in.StatusPollCommandEnabled = false
	}
	if in.PollIntervalMs <= 0 {
		in.PollIntervalMs = DefaultPollIntervalMs
	}
	if in.StatusPollIntervalMs <= 0 {
		in.StatusPollIntervalMs = in.PollIntervalMs
	}
	return in
}

func validatedSettings(in, defaults Settings) (Settings, error) {
	out := normalizeSettings(in, defaults)
	if err := validateSerialPort(out.SerialPort); err != nil {
		return Settings{}, err
	}
	if err := validateAmplifierTemperatureUnit(out.AmplifierTemperatureUnit); err != nil {
		return Settings{}, err
	}
	if err := validateLabel("panelModelLabel", out.PanelModelLabel); err != nil {
		return Settings{}, err
	}
	if err := validateLabelMap("inputLabels", out.InputLabels, 2); err != nil {
		return Settings{}, err
	}
	if err := validateLabelMap("antennaLabels", out.AntennaLabels, 6); err != nil {
		return Settings{}, err
	}
	if err := validateSafetySettings(out); err != nil {
		return Settings{}, err
	}
	if err := validateAutomaticFanPolicySettings(out); err != nil {
		return Settings{}, err
	}
	return out, nil
}

func validateAmplifierTemperatureUnit(value string) error {
	if _, ok := tempunit.Normalize(value); !ok {
		return errors.New("amplifier temperature unit must be C or F")
	}
	return nil
}

func validateSafetySettings(settings Settings) error {
	tempConfigured, err := validateThresholdPair("temperature", settings.TemperatureWarningC, settings.TemperatureTripC)
	if err != nil {
		return err
	}
	swrConfigured, err := validateThresholdPair("SWR", settings.SWRWarning, settings.SWRTrip)
	if err != nil {
		return err
	}
	if settings.SafetyMonitoringEnabled && !tempConfigured && !swrConfigured {
		return errors.New("safety monitoring requires a complete temperature or SWR threshold pair")
	}
	if settings.TemperatureResetC < 0 || math.IsNaN(settings.TemperatureResetC) || math.IsInf(settings.TemperatureResetC, 0) {
		return errors.New("temperature reset threshold must be a finite non-negative number")
	}
	if settings.TemperatureResetC > 0 {
		if !tempConfigured {
			return errors.New("temperature reset threshold requires a complete temperature threshold pair")
		}
		if settings.TemperatureResetC >= settings.TemperatureTripC {
			return errors.New("temperature reset threshold must be lower than the trip threshold")
		}
	}
	if settings.OvertemperatureStandbyArmed {
		if !settings.SafetyMonitoringEnabled {
			return errors.New("overtemperature standby requires safety monitoring to be enabled")
		}
		if !tempConfigured {
			return errors.New("overtemperature standby requires a complete temperature threshold pair")
		}
	}
	return nil
}

func validateThresholdPair(name string, warning, trip float64) (bool, error) {
	if math.IsNaN(warning) || math.IsInf(warning, 0) || math.IsNaN(trip) || math.IsInf(trip, 0) {
		return false, fmt.Errorf("%s thresholds must be finite numbers", name)
	}
	if warning == 0 && trip == 0 {
		return false, nil
	}
	if warning <= 0 || trip <= 0 {
		return false, fmt.Errorf("%s warning and trip thresholds must both be greater than zero", name)
	}
	if warning >= trip {
		return false, fmt.Errorf("%s warning threshold must be lower than its trip threshold", name)
	}
	return true, nil
}

func validateAutomaticFanPolicySettings(settings Settings) error {
	high := settings.FanHighTemperatureC
	normal := settings.FanNormalTemperatureC
	if math.IsNaN(high) || math.IsInf(high, 0) || math.IsNaN(normal) || math.IsInf(normal, 0) {
		return errors.New("automatic fan policy thresholds must be finite numbers")
	}
	if high <= 0 || normal <= 0 {
		return errors.New("automatic fan policy thresholds must both be greater than zero")
	}
	if normal >= high {
		return errors.New("fan normal temperature must be lower than fan high-cooling temperature")
	}
	if high-normal < MinimumFanHysteresisC {
		return fmt.Errorf("automatic fan policy thresholds must be separated by at least %.0f C", float64(MinimumFanHysteresisC))
	}
	if settings.FanBoostDurationMinutes < 0 || settings.FanBoostDurationMinutes > 10080 {
		return errors.New("fan boost duration must be between 0 and 10080 minutes")
	}
	if !settings.AutomaticFanPolicyEnabled && !settings.VerifyFanModeOnStartup {
		return nil
	}
	if !settings.DisplayPollingEnabled || !settings.StatusPollingEnabled || !settings.StatusPollCommandEnabled {
		return errors.New("fan policy control and startup verification require polling mode both")
	}
	if settings.OvertemperatureStandbyArmed &&
		settings.TemperatureTripC > 0 &&
		high >= settings.TemperatureTripC {
		return errors.New("fan high-cooling temperature must be lower than the armed overtemperature standby trip")
	}
	return nil
}

func normalizeLabel(value string) string {
	return strings.TrimSpace(value)
}

func normalizeLabelMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = normalizeLabel(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validateLabel(field, value string) error {
	if len(value) > 32 {
		return fmt.Errorf("%s is too long", field)
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) {
			return fmt.Errorf("%s contains unsupported control characters", field)
		}
	}
	return nil
}

func validateLabelMap(field string, labels map[string]string, maxIndex int) error {
	for key, value := range labels {
		idx := 0
		for _, r := range key {
			if r < '0' || r > '9' {
				return fmt.Errorf("%s has unsupported key %q", field, key)
			}
			idx = idx*10 + int(r-'0')
		}
		if idx < 1 || idx > maxIndex {
			return fmt.Errorf("%s key %q is out of range", field, key)
		}
		if err := validateLabel(field+"["+key+"]", value); err != nil {
			return err
		}
	}
	return nil
}

func validateSerialPort(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 256 {
		return fmt.Errorf("serialPort is too long")
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			return fmt.Errorf("serialPort must be a single path or port name")
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("serialPort contains unsupported control characters")
		}
	}
	return nil
}

func normalizeFanPolicyRuntimeState(in FanPolicyRuntimeState) FanPolicyRuntimeState {
	out := FanPolicyRuntimeState{
		ManualOverride:                strings.ToLower(strings.TrimSpace(in.ManualOverride)),
		ManualOverrideDurationMinutes: in.ManualOverrideDurationMinutes,
		ManualOverrideUntil:           strings.TrimSpace(in.ManualOverrideUntil),
		LastVerifiedPolicy:            strings.ToLower(strings.TrimSpace(in.LastVerifiedPolicy)),
		LastVerifiedAt:                strings.TrimSpace(in.LastVerifiedAt),
		LastVerifiedSource:            strings.TrimSpace(in.LastVerifiedSource),
		LastVerifiedModel:             strings.TrimSpace(in.LastVerifiedModel),
	}
	if out.ManualOverride != "normal" && out.ManualOverride != "high-cooling" {
		out.ManualOverride = ""
		out.ManualOverrideDurationMinutes = 0
		out.ManualOverrideUntil = ""
	}
	if out.ManualOverrideDurationMinutes < 0 || out.ManualOverrideDurationMinutes > 10080 {
		out.ManualOverrideDurationMinutes = 0
	}
	if out.ManualOverrideUntil != "" {
		out.ManualOverrideDurationMinutes = 0
	}
	if out.ManualOverrideUntil != "" {
		if _, err := time.Parse(time.RFC3339, out.ManualOverrideUntil); err != nil {
			out.ManualOverrideUntil = ""
		}
	}
	if out.LastVerifiedPolicy != "normal" && out.LastVerifiedPolicy != "high-cooling" {
		out.LastVerifiedPolicy = ""
		out.LastVerifiedAt = ""
		out.LastVerifiedSource = ""
		out.LastVerifiedModel = ""
	}
	if out.LastVerifiedAt != "" {
		if _, err := time.Parse(time.RFC3339, out.LastVerifiedAt); err != nil {
			out.LastVerifiedPolicy = ""
			out.LastVerifiedAt = ""
			out.LastVerifiedSource = ""
			out.LastVerifiedModel = ""
		}
	}
	return out
}

func writeFile(path string, settings Settings, fanPolicyState FanPolicyRuntimeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	if fanPolicyState != (FanPolicyRuntimeState{}) {
		document["fanPolicyState"] = fanPolicyState
	}
	data, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
