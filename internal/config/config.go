package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

const DefaultPollIntervalMs = 200

type Settings struct {
	SerialPort            string `json:"serialPort"`
	ListenAddress         string `json:"listenAddress"`
	PollIntervalMs        int    `json:"pollIntervalMs"`
	DisplayPollingEnabled bool   `json:"displayPollingEnabled"`
	StatusPollingEnabled  bool   `json:"statusPollingEnabled"`

	// Cosmetic station labels for the built-in web UI. These do not change
	// protocol/status identity or button behavior.
	PanelModelLabel string            `json:"panelModelLabel,omitempty"`
	InputLabels     map[string]string `json:"inputLabels,omitempty"`
	AntennaLabels   map[string]string `json:"antennaLabels,omitempty"`

	// Serial connection parameters for live ingest.
	SerialBaudRate           int  `json:"serialBaudRate,omitempty"`
	SerialReadTimeoutMs      int  `json:"serialReadTimeoutMs,omitempty"`
	StatusPollCommandEnabled bool `json:"statusPollCommandEnabled,omitempty"`
	StatusPollIntervalMs     int  `json:"statusPollIntervalMs,omitempty"`
	SerialAssertDTR          bool `json:"serialAssertDTR,omitempty"`
	SerialAssertRTS          bool `json:"serialAssertRTS,omitempty"`
}

type Snapshot struct {
	Settings   Settings `json:"settings"`
	NeedsSetup bool     `json:"needsSetup"`
	Path       string   `json:"path"`
}

type Manager struct {
	path string
	mu   sync.RWMutex
	cur  Settings
}

type rawSettings struct {
	SerialPort            *string           `json:"serialPort"`
	ListenAddress         *string           `json:"listenAddress"`
	PollIntervalMs        *int              `json:"pollIntervalMs"`
	DisplayPollingEnabled *bool             `json:"displayPollingEnabled"`
	StatusPollingEnabled  *bool             `json:"statusPollingEnabled"`
	PanelModelLabel       *string           `json:"panelModelLabel,omitempty"`
	InputLabels           map[string]string `json:"inputLabels,omitempty"`
	AntennaLabels         map[string]string `json:"antennaLabels,omitempty"`

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
		ListenAddress:         listenAddress,
		PollIntervalMs:        DefaultPollIntervalMs,
		DisplayPollingEnabled: true,
		StatusPollingEnabled:  true,

		// SPE Expert serial defaults.
		SerialBaudRate:           115200,
		SerialReadTimeoutMs:      250,
		StatusPollCommandEnabled: true,
		StatusPollIntervalMs:     125,
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
		return writeFile(m.path, m.cur)
	}

	var raw rawSettings
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.cur = raw.normalize(defaults)
	return writeFile(m.path, m.cur)
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
	m.cur = normalized
	if err := writeFile(m.path, m.cur); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Settings: m.cur, NeedsSetup: m.cur.SerialPort == "", Path: m.path}, nil
}

func (r rawSettings) normalize(defaults Settings) Settings {
	out := defaults
	if r.SerialPort != nil {
		out.SerialPort = *r.SerialPort
	}
	if r.ListenAddress != nil && *r.ListenAddress != "" {
		out.ListenAddress = *r.ListenAddress
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
	if r.PanelModelLabel != nil {
		out.PanelModelLabel = normalizeLabel(*r.PanelModelLabel)
	}
	out.InputLabels = normalizeLabelMap(r.InputLabels)
	out.AntennaLabels = normalizeLabelMap(r.AntennaLabels)
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
	if r.SerialAssertDTR != nil {
		out.SerialAssertDTR = *r.SerialAssertDTR
	}
	if r.SerialAssertRTS != nil {
		out.SerialAssertRTS = *r.SerialAssertRTS
	}
	return out
}

func normalizeSettings(in, defaults Settings) Settings {
	out := defaults
	out.SerialPort = strings.TrimSpace(in.SerialPort)
	if in.ListenAddress != "" {
		out.ListenAddress = strings.TrimSpace(in.ListenAddress)
	}
	if in.PollIntervalMs > 0 {
		out.PollIntervalMs = in.PollIntervalMs
	}
	out.DisplayPollingEnabled = in.DisplayPollingEnabled
	out.StatusPollingEnabled = in.StatusPollingEnabled
	out.PanelModelLabel = normalizeLabel(in.PanelModelLabel)
	out.InputLabels = normalizeLabelMap(in.InputLabels)
	out.AntennaLabels = normalizeLabelMap(in.AntennaLabels)
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
	return out
}

func validatedSettings(in, defaults Settings) (Settings, error) {
	out := normalizeSettings(in, defaults)
	if err := validateSerialPort(out.SerialPort); err != nil {
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
	return out, nil
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

func writeFile(path string, settings Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
