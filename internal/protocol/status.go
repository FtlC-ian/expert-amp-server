package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
)

var StatusPollCommand = []byte{0x55, 0x55, 0x55, 0x01, 0x90, 0x90}
var DisplayPollCommand = []byte{0x55, 0x55, 0x55, 0x01, 0x80, 0x80}
var StatusResponsePrefix = []byte{0xAA, 0xAA, 0xAA, 0x43}
var (
	statusFrameTerminator75 = []byte{'\r', '\n'}
	statusFrameTerminator76 = []byte{',', '\r', '\n'}
)

const (
	statusDataLen     = 67
	statusMinFrameLen = 3 + 1 + statusDataLen + 2 + 2
	statusMaxFrameLen = 3 + 1 + statusDataLen + 2 + 3
)

type StatusResponse struct {
	RawData         string
	PAIdentifier    string
	OperatingCode   string
	TXRXCode        string
	MemoryBank      string
	Input           string
	BandCode        string
	TXAntenna       string
	ATUStatusCode   string
	RXAntenna       string
	OutputLevelCode string
	OutputPowerRaw  string
	SWRATURaw       string
	SWRANTRaw       string
	VPARaw          string
	IPARaw          string
	TempUpperRaw    string
	TempLowerRaw    string
	TempCombRaw     string
	WarningCode     string
	AlarmCode       string
}

func IsStatusFrame(frame []byte) bool {
	return len(frame) >= len(StatusResponsePrefix) && bytes.Equal(frame[:len(StatusResponsePrefix)], StatusResponsePrefix)
}

func ParseStatusFrame(frame []byte) (StatusResponse, error) {
	if len(frame) < statusMinFrameLen || len(frame) > statusMaxFrameLen {
		return StatusResponse{}, fmt.Errorf("status frame length %d, want %d or %d", len(frame), statusMinFrameLen, statusMaxFrameLen)
	}
	if !IsStatusFrame(frame) {
		return StatusResponse{}, errors.New("not a status frame")
	}
	switch len(frame) {
	case statusMinFrameLen:
		if !bytes.Equal(frame[len(frame)-len(statusFrameTerminator75):], statusFrameTerminator75) {
			return StatusResponse{}, errors.New("status frame missing CRLF terminator")
		}
	case statusMaxFrameLen:
		if !bytes.Equal(frame[len(frame)-len(statusFrameTerminator76):], statusFrameTerminator76) {
			return StatusResponse{}, errors.New("status frame missing ,CRLF terminator")
		}
	default:
		return StatusResponse{}, fmt.Errorf("status frame length %d, want %d or %d", len(frame), statusMinFrameLen, statusMaxFrameLen)
	}
	data := frame[4 : 4+statusDataLen]
	chk0 := int(frame[4+statusDataLen])
	chk1 := int(frame[4+statusDataLen+1])
	sum := 0
	for _, b := range data {
		sum += int(b)
	}
	if chk0 != sum%256 || chk1 != sum/256 {
		return StatusResponse{}, fmt.Errorf("status frame checksum mismatch got (%d,%d) want (%d,%d)", chk0, chk1, sum%256, sum/256)
	}
	fields := normalizeStatusFields(strings.Split(string(data), ","))
	if len(fields) != 19 {
		return StatusResponse{}, fmt.Errorf("status field count %d, want 19", len(fields))
	}
	trim := func(s string) string { return strings.TrimSpace(s) }
	resp := StatusResponse{
		RawData:         string(data),
		PAIdentifier:    trim(fields[0]),
		OperatingCode:   trim(fields[1]),
		TXRXCode:        trim(fields[2]),
		MemoryBank:      trim(fields[3]),
		Input:           trim(fields[4]),
		BandCode:        trim(fields[5]),
		OutputLevelCode: trim(fields[8]),
		OutputPowerRaw:  trim(fields[9]),
		SWRATURaw:       trim(fields[10]),
		SWRANTRaw:       trim(fields[11]),
		VPARaw:          trim(fields[12]),
		IPARaw:          trim(fields[13]),
		TempUpperRaw:    trim(fields[14]),
		TempLowerRaw:    trim(fields[15]),
		TempCombRaw:     trim(fields[16]),
		WarningCode:     trim(fields[17]),
		AlarmCode:       trim(fields[18]),
	}
	if txAnt := trim(fields[6]); txAnt != "" {
		if len(txAnt) >= 1 {
			resp.TXAntenna = txAnt[:1]
		}
		if len(txAnt) >= 2 {
			resp.ATUStatusCode = txAnt[1:2]
		}
	}
	if rxAnt := trim(fields[7]); rxAnt != "" {
		resp.RXAntenna = rxAnt
	}
	return resp, nil
}

func normalizeStatusFields(fields []string) []string {
	if len(fields) == 19 {
		return fields
	}
	if len(fields) == 21 && fields[0] == "" && fields[len(fields)-1] == "" {
		return fields[1 : len(fields)-1]
	}
	return fields
}

func StatusFromFrame(frame []byte, source string) (api.Status, error) {
	resp, err := ParseStatusFrame(frame)
	if err != nil {
		return api.Status{}, err
	}
	return StatusFromResponse(resp, source), nil
}

func StatusFromResponse(resp StatusResponse, source string) api.Status {
	status := api.Status{
		Telemetry: api.Telemetry{
			Source:     source,
			Confidence: "protocol-native",
			Provenance: "status-poll",
		},
		BandCode:      resp.BandCode,
		BandText:      bandTextFromCode(resp.BandCode),
		RXAntenna:     zeroIf(resp.RXAntenna, "0r"),
		WarningCode:   zeroIf(resp.WarningCode, "N"),
		AlarmCode:     zeroIf(resp.AlarmCode, "N"),
		ATUStatusCode: resp.ATUStatusCode,
	}

	if model := modelNameFromIdentifier(resp.PAIdentifier); model != "" {
		status.ModelName = model
	}
	if op := operatingStateFromCode(resp.OperatingCode); op != "" {
		status.OperatingState = op
		status.Mode = op
	}
	if tx, ok := txBoolFromCode(resp.TXRXCode); ok {
		status.TX = &tx
	}
	if bank := normalizeMemoryBank(resp.MemoryBank); bank != "" {
		status.AntennaBank = bank
	}
	if input := normalizeDigit(resp.Input); input != "" {
		status.Input = input
	}
	if ant := normalizeDigit(resp.TXAntenna); ant != "" {
		status.Antenna = ant
	}
	if level := outputLevelFromCode(resp.OutputLevelCode); level != "" {
		status.OutputLevel = level
	}
	if p, ok := parseIntFloat(resp.OutputPowerRaw); ok {
		status.PowerWatts = &p
	}
	if swr, ok := parseFloatRaw(resp.SWRATURaw); ok {
		status.SWR = &swr
		status.SWRDisplay = fmt.Sprintf("%.2f", swr)
	} else if resp.SWRATURaw != "" {
		status.SWRDisplay = resp.SWRATURaw
	}
	if swrAnt, ok := parseFloatRaw(resp.SWRANTRaw); ok {
		status.AntennaSWR = &swrAnt
		status.AntennaSWRDisplay = fmt.Sprintf("%.2f", swrAnt)
	} else if resp.SWRANTRaw != "" {
		status.AntennaSWRDisplay = resp.SWRANTRaw
	}
	if vpa, ok := parseFloatRaw(resp.VPARaw); ok {
		status.PASupplyVoltage = &vpa
		status.PASupplyVoltageDisplay = fmt.Sprintf("%.1f", vpa)
	} else if resp.VPARaw != "" {
		status.PASupplyVoltageDisplay = resp.VPARaw
	}
	if ipa, ok := parseFloatRaw(resp.IPARaw); ok {
		status.PACurrent = &ipa
		status.PACurrentDisplay = fmt.Sprintf("%.1f", ipa)
	} else if resp.IPARaw != "" {
		status.PACurrentDisplay = resp.IPARaw
	}
	if t, ok := parseIntFloat(resp.TempUpperRaw); ok {
		status.TemperatureC = &t
		status.TemperatureDisplay = formatWholeNumberTemperatureDisplay(t)
	}
	if lower, ok := parseIntFloat(resp.TempLowerRaw); ok {
		status.TemperatureLowerC = &lower
		status.TemperatureLowerDisplay = formatWholeNumberTemperatureDisplay(lower)
	} else if raw := zeroIf(resp.TempLowerRaw, "0"); raw != "" {
		status.TemperatureLowerDisplay = raw
	}
	if comb, ok := parseIntFloat(resp.TempCombRaw); ok {
		status.TemperatureCombinerC = &comb
		status.TemperatureCombinerDisplay = formatWholeNumberTemperatureDisplay(comb)
	} else if raw := zeroIf(resp.TempCombRaw, "0"); raw != "" {
		status.TemperatureCombinerDisplay = raw
	}
	if warnings := warningsFromCode(resp.WarningCode); len(warnings) > 0 {
		status.WarningsText = warnings
		status.Warnings = append([]string(nil), warnings...)
	}
	if alarms := alarmsFromCode(resp.AlarmCode); len(alarms) > 0 {
		status.AlarmsText = alarms
		status.ActiveAlarms = append([]string(nil), alarms...)
	}
	return status
}

func modelNameFromIdentifier(id string) string {
	switch strings.ToUpper(strings.TrimSpace(id)) {
	case "20K":
		return "EXPERT 2K-FA"
	case "13K":
		return "EXPERT 1.3K-FA"
	case "15K":
		return "EXPERT 1.5K-FA"
	default:
		return ""
	}
}

func operatingStateFromCode(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "S":
		return "standby"
	case "O":
		return "operate"
	default:
		return ""
	}
}

func txBoolFromCode(code string) (bool, bool) {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "R":
		return false, true
	case "T":
		return true, true
	default:
		return false, false
	}
}

func normalizeMemoryBank(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "x") {
		return ""
	}
	return strings.ToUpper(v)
}

func normalizeDigit(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	return v
}

func outputLevelFromCode(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "L":
		return "LOW"
	case "M":
		return "MID"
	case "H":
		return "HIGH"
	default:
		return ""
	}
}

func parseFloatRaw(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func parseIntFloat(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	i, err := strconv.Atoi(v)
	if err == nil {
		return float64(i), true
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func bandTextFromCode(code string) string {
	switch strings.TrimSpace(code) {
	case "00":
		return "160m"
	case "01":
		return "80m"
	case "02":
		return "60m"
	case "03":
		return "40m"
	case "04":
		return "30m"
	case "05":
		return "20m"
	case "06":
		return "17m"
	case "07":
		return "15m"
	case "08":
		return "12m"
	case "09":
		return "10m"
	case "10":
		return "6m"
	case "11":
		return "4m"
	default:
		return ""
	}
}

func warningsFromCode(code string) []string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "", "N":
		return nil
	case "M":
		return []string{"amplifier alarm active"}
	case "A":
		return []string{"no selected antenna"}
	case "S":
		return []string{"swr antenna"}
	case "B":
		return []string{"no valid band"}
	case "P":
		return []string{"power limit exceeded"}
	case "O":
		return []string{"overheating"}
	case "Y":
		return []string{"atu not available"}
	case "W":
		return []string{"tuning with no power"}
	case "K":
		return []string{"atu bypassed"}
	case "R":
		return []string{"power switch held by remote"}
	case "T":
		return []string{"combiner overheating"}
	case "C":
		return []string{"combiner fault"}
	default:
		return []string{"warning code " + strings.TrimSpace(code)}
	}
}

func alarmsFromCode(code string) []string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "", "N":
		return nil
	case "S":
		return []string{"swr exceeding limits"}
	case "A":
		return []string{"amplifier protection"}
	case "D":
		return []string{"input overdriving"}
	case "H":
		return []string{"excess overheating"}
	case "C":
		return []string{"combiner fault"}
	default:
		return []string{"alarm code " + strings.TrimSpace(code)}
	}
}

func zeroIf(v string, zero string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, zero) {
		return ""
	}
	return v
}

func formatWholeNumberTemperatureDisplay(v float64) string {
	return fmt.Sprintf("%.0f C", v)
}

type StatusStreamDecoder struct {
	buf []byte
}

func NewStatusStreamDecoder() *StatusStreamDecoder { return &StatusStreamDecoder{} }

func (d *StatusStreamDecoder) Push(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	d.buf = append(d.buf, chunk...)
	var frames [][]byte
	for {
		start := bytes.Index(d.buf, StatusResponsePrefix)
		if start < 0 {
			if len(d.buf) > len(StatusResponsePrefix)-1 {
				d.buf = append([]byte(nil), d.buf[len(d.buf)-(len(StatusResponsePrefix)-1):]...)
			}
			break
		}
		if start > 0 {
			d.buf = d.buf[start:]
			start = 0
		}

		end := bytes.Index(d.buf, []byte("\r\n"))
		if end < 0 {
			break
		}
		end += len("\r\n")
		if end < statusMinFrameLen {
			d.buf = d.buf[1:]
			continue
		}
		frame := append([]byte(nil), d.buf[:end]...)
		if _, err := ParseStatusFrame(frame); err == nil {
			frames = append(frames, frame)
			d.buf = d.buf[end:]
			continue
		}
		d.buf = d.buf[1:]
	}
	return frames
}
