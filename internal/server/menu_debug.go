package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/config"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/fanpolicy"
	"github.com/FtlC-ian/expert-amp-server/internal/menudebug"
)

const menuDebugTokenHeader = "X-Menu-Debug-Token"

type MenuDebugUploader interface {
	Upload(context.Context, menudebug.Report) error
}

type menuDebugAPI struct {
	mu           sync.Mutex
	opts         Options
	firmware     string
	capabilities []menudebug.Capability
	uploads      map[string]bool
}

type menuDebugArmRequest struct {
	Acknowledgement string   `json:"acknowledgement"`
	FirmwareVersion string   `json:"firmwareVersion"`
	Capabilities    []string `json:"capabilities"`
}

type menuDebugRevisionRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type menuDebugAdvanceRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	Confirmation     string `json:"confirmation"`
}

type menuDebugVerificationRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	Phase            string `json:"phase"`
	Verified         bool   `json:"verified"`
	Note             string `json:"note,omitempty"`
}

type menuDebugUploadRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
	Consent          bool   `json:"consent"`
}

type menuDebugPrerequisites struct {
	Standby                        bool `json:"standby"`
	RX                             bool `json:"rx"`
	RecentContact                  bool `json:"recentContact"`
	StandbyHome                    bool `json:"standbyHome"`
	AutomaticFanPolicyDisabled     bool `json:"automaticFanPolicyDisabled"`
	OvertemperatureStandbyDisarmed bool `json:"overtemperatureStandbyDisarmed"`
	ActuationLeaseAvailable        bool `json:"actuationLeaseAvailable"`
}

type menuDebugProposal struct {
	Action       string `json:"action,omitempty"`
	Description  string `json:"description,omitempty"`
	Confirmation string `json:"confirmation,omitempty"`
}

type menuDebugSessionResponse struct {
	Session                menudebug.SessionView  `json:"session"`
	Prerequisites          menuDebugPrerequisites `json:"prerequisites"`
	Proposal               menuDebugProposal      `json:"proposal,omitempty"`
	ReportAvailable        bool                   `json:"reportAvailable"`
	FanOverrideAutoCleared bool                   `json:"fanOverrideAutoCleared,omitempty"`
	Token                  string                 `json:"token,omitempty"`
}

func registerMenuDebugRoutes(mux *http.ServeMux, opts Options) {
	api := &menuDebugAPI{opts: opts}
	mux.HandleFunc("/api/v1/menu-debug/session", api.session)
	mux.HandleFunc("/api/v1/menu-debug/session/advance", api.advance)
	mux.HandleFunc("/api/v1/menu-debug/session/verification", api.verification)
	mux.HandleFunc("/api/v1/menu-debug/session/abort", api.abort)
	mux.HandleFunc("/api/v1/menu-debug/report", api.report)
	mux.HandleFunc("/api/v1/menu-debug/report.json", api.download)
	mux.HandleFunc("/api/v1/menu-debug/report/upload", api.upload)
}

func (m *menuDebugAPI) session(w http.ResponseWriter, r *http.Request) {
	if m.opts.MenuDebug == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "menu-debug controller unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		view, err := m.opts.MenuDebug.Current(r.Header.Get(menuDebugTokenHeader))
		if err != nil && view.Phase != menudebug.PhaseIdle {
			m.writeConflict(w, view, err)
			return
		}
		writeAPI(w, http.StatusOK, api.Response{Success: true, Data: m.response(view, "")})
	case http.MethodPost:
		var req menuDebugArmRequest
		if err := decodeStrictJSON(r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		capabilities, err := parseMenuDebugCapabilities(req.Capabilities)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		firmware := strings.TrimSpace(req.FirmwareVersion)
		if !validFirmwareVersion(firmware) {
			writeAPIError(w, http.StatusBadRequest, "firmwareVersion must be unknown or a version beginning with a number, v, FW, or firmware and must not contain an address, path, URL, hostname, or callsign")
			return
		}
		pre, _ := m.prerequisites()
		fanOverrideAutoCleared := false
		settings := config.Settings{}
		mayAutoClearNormal := pre.ManualFanControlActive && m.opts.FanPolicy != nil && m.opts.Config != nil
		if m.opts.Config != nil {
			settings = m.opts.Config.Get().Settings
			mayAutoClearNormal = mayAutoClearNormal && !settings.AutomaticFanPolicyEnabled
		}
		armPre := pre
		if mayAutoClearNormal && strings.TrimSpace(req.Acknowledgement) == menudebug.Acknowledgement && menudebug.ValidatePrerequisitesIgnoringFanControl(pre) == nil {
			armPre.AutomaticFanPolicyActive = false
			armPre.ManualFanControlActive = false
		}
		view, token, err := m.opts.MenuDebug.Arm(req.Acknowledgement, armPre)
		if err != nil {
			m.writeConflict(w, view, err)
			return
		}
		if mayAutoClearNormal {
			status := m.opts.MenuDebug.Runtime().Status
			fanOverrideAutoCleared, err = m.opts.FanPolicy.ClearCompletedNormalOverride(status, fanPolicySettings(settings))
			if err != nil || !fanOverrideAutoCleared {
				aborted, abortErr := m.opts.MenuDebug.Abort(token, view.Revision, true)
				if abortErr != nil {
					m.writeConflict(w, aborted, fmt.Errorf("fan control remains active and provisional menu-debug arm could not be cancelled: %w", abortErr))
					return
				}
				if err == nil {
					err = errors.New("fan control must be inactive")
				}
				m.writeConflict(w, aborted, err)
				return
			}
		}
		m.mu.Lock()
		m.firmware = firmware
		m.capabilities = capabilities
		m.mu.Unlock()
		response := m.response(view, token)
		response.FanOverrideAutoCleared = fanOverrideAutoCleared
		message := "menu-debug session armed; amplifier remains in STANDBY"
		if fanOverrideAutoCleared {
			message = "menu-debug session armed; completed Normal fan override cleared without sending an amplifier command"
		}
		writeAPI(w, http.StatusCreated, api.Response{Success: true, Message: message, Data: response})
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (m *menuDebugAPI) advance(w http.ResponseWriter, r *http.Request) {
	if !allowMethodAPI(w, r, http.MethodPost) {
		return
	}
	if !m.available(w) {
		return
	}
	var req menuDebugAdvanceRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := r.Header.Get(menuDebugTokenHeader)
	view, err := m.opts.MenuDebug.Current(token)
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	if req.ExpectedRevision != view.Revision {
		m.writeConflict(w, view, errors.New("stale or replayed menu-debug session revision"))
		return
	}
	switch view.Phase {
	case menudebug.PhaseArmed:
		if req.Confirmation != "begin-discovery" {
			writeAPIError(w, http.StatusBadRequest, "confirmation must be begin-discovery")
			return
		}
		capability, ok := m.nextCapability(view.Completed)
		if !ok {
			view, err = m.opts.MenuDebug.Complete(token, view.Revision)
			break
		}
		view, err = m.opts.MenuDebug.Begin(token, view.Revision, capability)
		if err == nil {
			view, err = m.sendDiscovery(r.Context(), token, view)
		}
	case menudebug.PhaseDiscovering:
		if req.Confirmation != "begin-discovery" {
			writeAPIError(w, http.StatusBadRequest, "confirmation must be begin-discovery")
			return
		}
		view, err = m.sendDiscovery(r.Context(), token, view)
	case menudebug.PhasePlanReady:
		if req.Confirmation != "apply-proposed-change" {
			writeAPIError(w, http.StatusBadRequest, "confirmation must be apply-proposed-change")
			return
		}
		view, err = m.opts.MenuDebug.BeginApply(token, view.Revision)
		if err == nil {
			view, err = m.sendPlanned(r.Context(), token, view)
		}
	case menudebug.PhaseApplying:
		if req.Confirmation != "apply-proposed-change" {
			writeAPIError(w, http.StatusBadRequest, "confirmation must be apply-proposed-change")
			return
		}
		view, err = m.sendPlanned(r.Context(), token, view)
	case menudebug.PhaseRestoring:
		if req.Confirmation != "restore-original" {
			writeAPIError(w, http.StatusBadRequest, "confirmation must be restore-original")
			return
		}
		view, err = m.sendPlanned(r.Context(), token, view)
	default:
		err = fmt.Errorf("session phase %s has no advance action", view.Phase)
	}
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	writeAPI(w, http.StatusAccepted, api.Response{Success: true, Message: "bounded menu-debug action accepted; waiting for newer display evidence", Data: m.response(view, "")})
}

func (m *menuDebugAPI) verification(w http.ResponseWriter, r *http.Request) {
	if !allowMethodAPI(w, r, http.MethodPost) {
		return
	}
	if !m.available(w) {
		return
	}
	var req menuDebugVerificationRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := r.Header.Get(menuDebugTokenHeader)
	view, err := m.opts.MenuDebug.Current(token)
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	if req.ExpectedRevision != view.Revision {
		m.writeConflict(w, view, errors.New("stale or replayed menu-debug session revision"))
		return
	}
	if (req.Phase == "candidate" && view.Phase != menudebug.PhaseAwaitingApplyVerify) || (req.Phase == "restored" && view.Phase != menudebug.PhaseAwaitingRestoreVerify) {
		writeAPIError(w, http.StatusBadRequest, "verification phase does not match the current session")
		return
	}
	view, err = m.opts.MenuDebug.Confirm(token, view.Revision, req.Verified)
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	if req.Phase == "candidate" {
		view, err = m.opts.MenuDebug.BeginRestore(token, view.Revision)
	} else if _, hasNext := m.nextCapability(view.Completed); !hasNext {
		view, err = m.opts.MenuDebug.Complete(token, view.Revision)
	}
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	writeAPI(w, http.StatusOK, api.Response{Success: true, Message: "operator verification recorded", Data: m.response(view, "")})
}

func (m *menuDebugAPI) abort(w http.ResponseWriter, r *http.Request) {
	if !allowMethodAPI(w, r, http.MethodPost) {
		return
	}
	if !m.available(w) {
		return
	}
	var req menuDebugRevisionRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := r.Header.Get(menuDebugTokenHeader)
	view, err := m.opts.MenuDebug.Current(token)
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	if req.ExpectedRevision != view.Revision {
		m.writeConflict(w, view, errors.New("stale or replayed menu-debug session revision"))
		return
	}
	runtime := m.opts.MenuDebug.Runtime()
	atHome := runtime.ChecksumValid && runtime.Screen.Kind == menudebug.ScreenHome && boolIs(runtime.DisplayTX, false) && boolIs(runtime.DisplayOperate, false)
	view, err = m.opts.MenuDebug.Abort(token, view.Revision, atHome)
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	writeAPI(w, http.StatusOK, api.Response{Success: true, Message: "menu-debug session aborted; no recovery or OPERATE command was sent", Data: m.response(view, "")})
}

func (m *menuDebugAPI) report(w http.ResponseWriter, r *http.Request) {
	if !allowMethodAPI(w, r, http.MethodGet) {
		return
	}
	report, ok := m.authorizedReport(w, r)
	if !ok {
		return
	}
	writeAPI(w, http.StatusOK, api.Response{Success: true, Data: report})
}

func (m *menuDebugAPI) download(w http.ResponseWriter, r *http.Request) {
	if !allowMethodAPI(w, r, http.MethodGet) {
		return
	}
	report, ok := m.authorizedReport(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="expert-amp-menu-report.json"`)
	_ = json.NewEncoder(w).Encode(report)
}

func (m *menuDebugAPI) upload(w http.ResponseWriter, r *http.Request) {
	if !allowMethodAPI(w, r, http.MethodPost) {
		return
	}
	if !m.available(w) {
		return
	}
	var req menuDebugUploadRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Consent {
		writeAPIError(w, http.StatusBadRequest, "explicit upload consent is required")
		return
	}
	view, err := m.opts.MenuDebug.Current(r.Header.Get(menuDebugTokenHeader))
	if err != nil {
		m.writeConflict(w, view, err)
		return
	}
	if req.ExpectedRevision != view.Revision {
		m.writeConflict(w, view, errors.New("stale or replayed menu-debug session revision"))
		return
	}
	if m.opts.MenuDebugUploader == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "menu-debug report uploader is not configured")
		return
	}
	if view.Phase != menudebug.PhaseComplete {
		m.writeConflict(w, view, errors.New("menu-debug report upload requires a completed session"))
		return
	}
	report := m.currentReport()
	if len(report.Capabilities) == 0 {
		m.writeConflict(w, view, errors.New("menu-debug report has no completed capability evidence"))
		return
	}
	m.mu.Lock()
	if m.uploads == nil {
		m.uploads = make(map[string]bool)
	}
	if m.uploads[view.ID] {
		m.mu.Unlock()
		m.writeConflict(w, view, errors.New("this menu-debug report has already been uploaded"))
		return
	}
	m.uploads[view.ID] = true
	m.mu.Unlock()
	if err := m.opts.MenuDebugUploader.Upload(r.Context(), report); err != nil {
		m.mu.Lock()
		delete(m.uploads, view.ID)
		m.mu.Unlock()
		writeAPIError(w, http.StatusServiceUnavailable, fmt.Sprintf("menu-debug report upload failed: %v", err))
		return
	}
	writeAPI(w, http.StatusAccepted, api.Response{Success: true, Message: "sanitized menu-debug report uploaded"})
}

func (m *menuDebugAPI) sendDiscovery(ctx context.Context, token string, view menudebug.SessionView) (menudebug.SessionView, error) {
	runtime := m.opts.MenuDebug.Runtime()
	evidence := menuDebugEvidence(runtime, view.Capability)
	var action menudebug.Action
	switch runtime.Screen.Kind {
	case menudebug.ScreenHome:
		action = menudebug.ActionSet
	case menudebug.ScreenSetup:
		selected := strings.ToUpper(runtime.Screen.SelectedText)
		if (view.Capability == menudebug.CapabilityFan && strings.Contains(selected, "FAN")) || (view.Capability == menudebug.CapabilityBank && strings.Contains(selected, "BANK")) {
			action = menudebug.ActionSet
			evidence.Candidate = view.Capability
		} else {
			action = menudebug.ActionRight
		}
	case menudebug.ScreenFan, menudebug.ScreenBank:
		if action, ok := reviewedMenuDebugDiscoveryAction(runtime, view.Capability); ok {
			authorization, err := m.opts.MenuDebug.AuthorizeDiscovery(token, view.Revision, action, evidence)
			if err != nil {
				return view, err
			}
			if err := m.sendAuthorized(ctx, token, authorization, false); err != nil {
				failed, failErr := m.opts.MenuDebug.Fail(token, authorization.Revision, err.Error())
				if failErr != nil {
					return failed, failErr
				}
				return failed, err
			}
			return m.opts.MenuDebug.Current(token)
		}
		plan, planErr := reviewedMenuDebugPlan(runtime, view.Capability)
		if planErr != nil {
			if strings.Contains(planErr.Error(), "no reviewed action profile") {
				if reviewedMenuDebugNoSaveExit(runtime, view.Capability) {
					authorization, exitErr := m.opts.MenuDebug.AuthorizeTopologyExit(token, view.Revision, evidence)
					if exitErr != nil {
						return view, exitErr
					}
					if exitErr = m.sendAuthorized(ctx, token, authorization, false); exitErr != nil {
						failed, failErr := m.opts.MenuDebug.Fail(token, authorization.Revision, exitErr.Error())
						if failErr != nil {
							return failed, failErr
						}
						return failed, exitErr
					}
					return m.opts.MenuDebug.Current(token)
				}
				topologyView, topologyErr := m.opts.MenuDebug.CompleteTopology(token, view.Revision)
				if topologyErr != nil {
					return topologyView, topologyErr
				}
				return m.opts.MenuDebug.Complete(token, topologyView.Revision)
			}
			return view, planErr
		}
		return m.opts.MenuDebug.InstallPlan(token, view.Revision, plan)
	default:
		return view, errors.New("current display is not a recognized home or setup screen")
	}
	authorization, err := m.opts.MenuDebug.AuthorizeDiscovery(token, view.Revision, action, evidence)
	if err != nil {
		return view, err
	}
	if err := m.sendAuthorized(ctx, token, authorization, runtime.Screen.Kind == menudebug.ScreenHome); err != nil {
		failed, failErr := m.opts.MenuDebug.Fail(token, authorization.Revision, err.Error())
		if failErr != nil {
			return failed, failErr
		}
		return failed, err
	}
	return m.opts.MenuDebug.Current(token)
}

// reviewedMenuDebugNoSaveExit is deliberately model-scoped. Controlled live
// testing on the Expert 1.3K-FA confirmed that serial DISPLAY exits FAN and
// BANK menus to home without saving; unknown models retain operator recovery.
func reviewedMenuDebugNoSaveExit(runtime menudebug.RuntimeSnapshot, capability menudebug.Capability) bool {
	if !strings.EqualFold(strings.TrimSpace(runtime.Status.ModelName), "EXPERT 1.3K-FA") {
		return false
	}
	return (capability == menudebug.CapabilityFan && runtime.Screen.Kind == menudebug.ScreenFan) ||
		(capability == menudebug.CapabilityBank && runtime.Screen.Kind == menudebug.ScreenBank)
}

// reviewedMenuDebugDiscoveryAction authorizes only a selector move that is
// part of a reviewed model-and-topology profile. Both captured NORMAL/CONTEST
// profiles land on TEMPERATURE SCALE; one RIGHT selects FAN MANAGEMENT without
// changing a value. Unknown layouts never receive this authorization.
func reviewedMenuDebugDiscoveryAction(runtime menudebug.RuntimeSnapshot, capability menudebug.Capability) (menudebug.Action, bool) {
	if capability != menudebug.CapabilityFan {
		return "", false
	}
	if _, ok := reviewedFanProfileFor(runtime); !ok {
		return "", false
	}
	selected, _, exactLayout := fanpolicy.NormalContestFanScreen(runtime.DisplayState)
	if !exactLayout || selected != "TEMPERATURE SCALE" {
		return "", false
	}
	return menudebug.ActionRight, true
}

func (m *menuDebugAPI) sendPlanned(ctx context.Context, token string, view menudebug.SessionView) (menudebug.SessionView, error) {
	authorization, err := m.opts.MenuDebug.AuthorizeNext(token, view.Revision, menuDebugEvidence(m.opts.MenuDebug.Runtime(), view.Capability))
	if err != nil {
		return view, err
	}
	if err := m.sendAuthorized(ctx, token, authorization, false); err != nil {
		failed, failErr := m.opts.MenuDebug.Fail(token, authorization.Revision, err.Error())
		if failErr != nil {
			return failed, failErr
		}
		return failed, err
	}
	return m.opts.MenuDebug.Current(token)
}

func (m *menuDebugAPI) sendAuthorized(ctx context.Context, token string, authorization menudebug.ActionAuthorization, requireHome bool) error {
	if err := m.validateWritePrerequisites(requireHome); err != nil {
		return err
	}
	if m.opts.MenuDebugTransport == nil {
		return errors.New("menu-debug button transport unavailable")
	}
	view, err := m.opts.MenuDebug.Current(token)
	if err != nil {
		return err
	}
	if view.Revision != authorization.Revision {
		return errors.New("menu-debug session changed before the authorized write")
	}
	if authorization.ExpectedModel != "" && !strings.EqualFold(strings.TrimSpace(authorization.ExpectedModel), strings.TrimSpace(m.opts.MenuDebug.Runtime().Status.ModelName)) {
		return errors.New("connected amplifier model changed before the authorized write")
	}
	result, err := m.opts.MenuDebugTransport.SendButton(ctx, api.ButtonAction{Name: string(authorization.Action)})
	if err != nil {
		return err
	}
	if !result.Sent {
		return errors.New("menu-debug button command was not sent")
	}
	return nil
}

func (m *menuDebugAPI) validateWritePrerequisites(requireHome bool) error {
	pre, _ := m.prerequisites()
	switch {
	case !pre.DebugEnabled:
		return errors.New("menu debug mode was disabled before the write")
	case !pre.RecentProtocolStatus:
		return errors.New("fresh protocol contact was lost before the write")
	case !pre.ProtocolStandby || !pre.ProtocolRX:
		return errors.New("protocol no longer verifies STANDBY/RX")
	case !pre.ChecksumValidDisplay || !pre.DisplayStandby || !pre.DisplayRX:
		return errors.New("checksum-valid STANDBY/RX display evidence was lost before the write")
	case pre.AutomaticFanPolicyActive || pre.ManualFanControlActive:
		return errors.New("fan control became active before the write")
	case pre.OvertemperatureArmed:
		return errors.New("overtemperature actuation became armed before the write")
	case requireHome && !pre.HomeDisplay:
		return errors.New("verified STANDBY home display is required before entering setup")
	default:
		return nil
	}
}

func (m *menuDebugAPI) prerequisites() (menudebug.Prerequisites, menuDebugPrerequisites) {
	runtime := m.opts.MenuDebug.Runtime()
	settings := config.Settings{}
	if m.opts.Config != nil {
		settings = m.opts.Config.Get().Settings
	}
	const evidenceWindow = 5 * time.Second
	recent := runtime.Status.RecentContact && observationIsFresh(runtime.StatusObservedAt, evidenceWindow)
	displayRecent := observationIsFresh(runtime.DisplayObservedAt, evidenceWindow)
	standby := strings.EqualFold(runtime.Status.OperatingState, "standby")
	rx := boolIs(runtime.Status.TX, false)
	home := displayRecent && runtime.ChecksumValid && runtime.Screen.Kind == menudebug.ScreenHome && boolIs(runtime.DisplayTX, false) && boolIs(runtime.DisplayOperate, false)
	fanActive := settings.AutomaticFanPolicyEnabled
	if m.opts.FanPolicy != nil {
		fanActive = m.opts.FanPolicy.View(runtime.Status, fanPolicySettings(settings)).ControlActive
	}
	overtemp := settings.SafetyMonitoringEnabled && settings.OvertemperatureStandbyArmed
	pre := menudebug.Prerequisites{DebugEnabled: settings.MenuDebugEnabled, RecentProtocolStatus: recent, ProtocolStandby: standby, ProtocolRX: rx, ChecksumValidDisplay: displayRecent && runtime.ChecksumValid, DisplayStandby: displayRecent && boolIs(runtime.DisplayOperate, false), DisplayRX: displayRecent && boolIs(runtime.DisplayTX, false), HomeDisplay: displayRecent && runtime.Screen.Kind == menudebug.ScreenHome, AutomaticFanPolicyActive: fanActive, ManualFanControlActive: fanActive && !settings.AutomaticFanPolicyEnabled, OvertemperatureArmed: overtemp, DisplayGeneration: runtime.DisplayGeneration, StatusGeneration: runtime.StatusGeneration}
	return pre, menuDebugPrerequisites{Standby: standby, RX: rx, RecentContact: recent, StandbyHome: home, AutomaticFanPolicyDisabled: !fanActive, OvertemperatureStandbyDisarmed: !overtemp, ActuationLeaseAvailable: !fanActive && !overtemp}
}

func observationIsFresh(observedAt time.Time, window time.Duration) bool {
	if observedAt.IsZero() {
		return false
	}
	age := time.Since(observedAt)
	return age >= 0 && age <= window
}

func (m *menuDebugAPI) response(view menudebug.SessionView, token string) menuDebugSessionResponse {
	_, prerequisites := m.prerequisites()
	report := m.currentReport()
	return menuDebugSessionResponse{Session: view, Prerequisites: prerequisites, Proposal: proposalFor(view), ReportAvailable: len(report.Capabilities) > 0, Token: token}
}

func proposalFor(view menudebug.SessionView) menuDebugProposal {
	switch view.Phase {
	case menudebug.PhaseArmed, menudebug.PhaseDiscovering:
		return menuDebugProposal{Action: "continue bounded discovery", Description: "Send at most one server-authorized SET or RIGHT command, then wait for a newer verified display", Confirmation: "begin-discovery"}
	case menudebug.PhasePlanReady, menudebug.PhaseApplying:
		return menuDebugProposal{Action: "apply frozen plan", Description: "Send the next server-owned reviewed apply command and verify its newer display receipt", Confirmation: "apply-proposed-change"}
	case menudebug.PhaseRestoring:
		return menuDebugProposal{Action: "restore original", Description: "Send the next server-owned reviewed restore command and verify its newer display receipt", Confirmation: "restore-original"}
	default:
		return menuDebugProposal{}
	}
}

func (m *menuDebugAPI) currentReport() menudebug.Report {
	m.mu.Lock()
	firmware := m.firmware
	m.mu.Unlock()
	runtime := m.opts.MenuDebug.Runtime()
	model := sanitizeModel(runtime.Status.ModelName)
	serverVersion := sanitizeIdentity(selectedVersion(m.opts).Version)
	report := m.opts.MenuDebug.Report(model, sanitizeIdentity(firmware), serverVersion)
	for capabilityIndex := range report.Capabilities {
		capability := &report.Capabilities[capabilityIndex]
		capability.OriginalValue = redactReportText(capability.OriginalValue)
		capability.CandidateValue = redactReportText(capability.CandidateValue)
		for evidenceIndex := range capability.Evidence {
			evidence := &capability.Evidence[evidenceIndex]
			evidence.Selection = redactReportText(evidence.Selection)
			evidence.Value = redactReportText(evidence.Value)
			for row := range evidence.Rows {
				evidence.Rows[row] = redactReportText(evidence.Rows[row])
			}
		}
	}
	return report
}

func (m *menuDebugAPI) authorizedReport(w http.ResponseWriter, r *http.Request) (menudebug.Report, bool) {
	if !m.available(w) {
		return menudebug.Report{}, false
	}
	view, err := m.opts.MenuDebug.Current(r.Header.Get(menuDebugTokenHeader))
	if err != nil {
		m.writeConflict(w, view, err)
		return menudebug.Report{}, false
	}
	return m.currentReport(), true
}

func (m *menuDebugAPI) nextCapability(completed []menudebug.Capability) (menudebug.Capability, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, capability := range m.capabilities {
		found := false
		for _, done := range completed {
			if capability == done {
				found = true
				break
			}
		}
		if !found {
			return capability, true
		}
	}
	return "", false
}

func (m *menuDebugAPI) available(w http.ResponseWriter) bool {
	if m.opts.MenuDebug == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "menu-debug controller unavailable")
		return false
	}
	return true
}

func (m *menuDebugAPI) writeConflict(w http.ResponseWriter, view menudebug.SessionView, err error) {
	writeAPI(w, http.StatusConflict, api.Response{Success: false, Error: err.Error(), Data: m.response(view, "")})
}

func parseMenuDebugCapabilities(values []string) ([]menudebug.Capability, error) {
	if len(values) == 0 {
		return nil, errors.New("capabilities must contain fan and/or bank")
	}
	seen := map[menudebug.Capability]bool{}
	out := make([]menudebug.Capability, 0, len(values))
	for _, value := range values {
		capability := menudebug.Capability(strings.ToLower(strings.TrimSpace(value)))
		if capability != menudebug.CapabilityFan && capability != menudebug.CapabilityBank {
			return nil, errors.New("capabilities may contain only fan and bank")
		}
		if !seen[capability] {
			seen[capability] = true
			out = append(out, capability)
		}
	}
	return out, nil
}

func menuDebugEvidence(runtime menudebug.RuntimeSnapshot, capability menudebug.Capability) menudebug.Evidence {
	var rows [8]string
	copy(rows[:], runtime.Screen.Rows)
	candidate := menudebug.Capability(runtime.Screen.Capability)
	if strings.Contains(runtime.Screen.Capability, string(capability)) {
		candidate = capability
	}
	standbyHome := runtime.ChecksumValid && runtime.Screen.Kind == menudebug.ScreenHome && boolIs(runtime.DisplayTX, false) && boolIs(runtime.DisplayOperate, false)
	value := runtime.Screen.SelectedValue
	if runtime.Screen.ActiveValue != "" {
		value = runtime.Screen.ActiveValue
	}
	return menudebug.Evidence{Generation: runtime.DisplayGeneration, Fingerprint: runtime.Screen.Fingerprint, Kind: runtime.Screen.Kind, Rows: rows, Selection: runtime.Screen.SelectedText, Candidate: candidate, Value: value, SaveVisible: runtime.Screen.SaveVisible, StandbyHome: standbyHome, ObservedAt: runtime.DisplayObservedAt, SetupTopology: runtime.Screen.SetupTopology}
}

type reviewedFanProfile struct {
	ID                    string
	Model                 string
	SetupTopology         string
	RestoreSetupWaypoints []string
}

var reviewedFanProfiles = []reviewedFanProfile{
	{
		ID:                    "expert-1.3k-fa-first-series-fan-v1",
		Model:                 "EXPERT 1.3K-FA",
		SetupTopology:         menudebug.SetupTopologyFirstSeries,
		RestoreSetupWaypoints: []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "~BEEP", "~START", "TEMP/FANS"},
	},
	{
		ID:                    "expert-1.5k-fa-second-series-fan-v1",
		Model:                 "EXPERT 1.5K-FA",
		SetupTopology:         menudebug.SetupTopologySecondSeries,
		RestoreSetupWaypoints: []string{"CONFIG", "ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "~BEEP", "~START", "TEMP/FANS"},
	},
}

func reviewedFanProfileFor(runtime menudebug.RuntimeSnapshot) (reviewedFanProfile, bool) {
	if runtime.Screen.Kind != menudebug.ScreenFan {
		return reviewedFanProfile{}, false
	}
	if _, _, exactLayout := fanpolicy.NormalContestFanScreen(runtime.DisplayState); !exactLayout {
		return reviewedFanProfile{}, false
	}
	model := strings.TrimSpace(runtime.Status.ModelName)
	for _, profile := range reviewedFanProfiles {
		if strings.EqualFold(model, profile.Model) {
			return profile, true
		}
	}
	return reviewedFanProfile{}, false
}

func setupWaypointStep(action menudebug.Action, waypoint string) menudebug.Step {
	step := menudebug.Step{Action: action, ExpectedKind: menudebug.ScreenSetup}
	if action == menudebug.ActionSet {
		step.Purpose = menudebug.PurposeEnterCandidate
	} else {
		step.Purpose = menudebug.PurposeEnumerate
	}
	if strings.HasPrefix(waypoint, "~") {
		step.ExpectedSelectionContains = strings.TrimPrefix(waypoint, "~")
	} else {
		step.ExpectedSelection = waypoint
	}
	return step
}

func reviewedMenuDebugPlan(runtime menudebug.RuntimeSnapshot, capability menudebug.Capability) (menudebug.Plan, error) {
	if capability == menudebug.CapabilityBank {
		return reviewedFirstSeriesBankPlan(runtime)
	}
	profile, profileOK := reviewedFanProfileFor(runtime)
	if capability != menudebug.CapabilityFan || !profileOK {
		return menudebug.Plan{}, errors.New("topology captured, but this model/capability has no reviewed action profile; no value-changing or SAVE command is authorized")
	}
	selected, exactPolicy, exactLayout := fanpolicy.NormalContestFanScreen(runtime.DisplayState)
	if !exactLayout || selected != "FAN MANAGEMENT" {
		return menudebug.Plan{}, errors.New("topology captured, but this model/capability has no reviewed action profile; exact reviewed fan layout did not match")
	}
	original := strings.ToLower(strings.TrimSpace(runtime.Screen.SelectedValue))
	candidate := map[string]string{"normal": "contest", "contest": "normal"}[original]
	if candidate == "" || !strings.EqualFold(original, exactPolicy) {
		return menudebug.Plan{}, errors.New("the reviewed fan profile requires FAN MANAGEMENT with a classified NORMAL or CONTEST value")
	}
	fan := menudebug.ScreenFan
	home := menudebug.ScreenHome
	apply := []menudebug.Step{
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeChangeValue, FromFingerprint: runtime.Screen.Fingerprint, ExpectedKind: fan, ExpectedCapability: capability, ExpectedValue: candidate, ExpectedSelectionContains: "FAN MANAGEMENT"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: fan, ExpectedCapability: capability, ExpectedValue: candidate, ExpectedSelection: "SAVE", ExpectedSaveVisible: true},
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeSave, ExpectedKind: home, ExpectedStandbyHome: true, AllowStoringBeforeHome: true},
	}
	restore := make([]menudebug.Step, 0, len(profile.RestoreSetupWaypoints)+5)
	for index, waypoint := range profile.RestoreSetupWaypoints {
		action := menudebug.ActionRight
		if index == 0 {
			action = menudebug.ActionSet
		}
		restore = append(restore, setupWaypointStep(action, waypoint))
	}
	restore = append(restore,
		menudebug.Step{Action: menudebug.ActionSet, Purpose: menudebug.PurposeEnterCandidate, ExpectedKind: fan, ExpectedCapability: capability, ExpectedValue: candidate, ExpectedSelection: "TEMPERATURE SCALE"},
		menudebug.Step{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: fan, ExpectedCapability: capability, ExpectedValue: candidate, ExpectedSelectionContains: "FAN MANAGEMENT"},
		menudebug.Step{Action: menudebug.ActionSet, Purpose: menudebug.PurposeChangeValue, ExpectedKind: fan, ExpectedCapability: capability, ExpectedValue: original, ExpectedSelectionContains: "FAN MANAGEMENT"},
		menudebug.Step{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: fan, ExpectedCapability: capability, ExpectedValue: original, ExpectedSelection: "SAVE", ExpectedSaveVisible: true},
		menudebug.Step{Action: menudebug.ActionSet, Purpose: menudebug.PurposeSave, ExpectedKind: home, ExpectedStandbyHome: true, AllowStoringBeforeHome: true},
	)
	return menudebug.Plan{Profile: profile.ID, ExpectedModel: profile.Model, Capability: capability, OriginalValue: original, CandidateValue: candidate, DiscoverySetupWaypoints: append([]string(nil), profile.RestoreSetupWaypoints...), DiscoverySetupTopology: profile.SetupTopology, Apply: apply, Restore: restore}, nil
}

func reviewedFirstSeriesBankPlan(runtime menudebug.RuntimeSnapshot) (menudebug.Plan, error) {
	screen := runtime.Screen
	if screen.Kind != menudebug.ScreenBank || !strings.EqualFold(strings.TrimSpace(runtime.Status.ModelName), "EXPERT 1.3K-FA") ||
		len(screen.Values) != 2 || screen.Values[0] != "A" || screen.Values[1] != "B" || screen.SelectedText != "[ ] BNK A" || screen.SelectedValue != "A" || screen.ActiveValue != "A" ||
		!strings.EqualFold(strings.TrimSpace(runtime.Status.AntennaBank), "A") ||
		len(screen.Rows) != display.Rows || strings.TrimSpace(screen.Rows[0]) != "STORAGE MANAGEMENT" ||
		strings.TrimSpace(screen.Rows[6]) != "SET MEMORY BANK FOR ANTENNAS/ATU" || !strings.Contains(screen.Rows[7], "[SET]:CHANGE") || !screen.SaveVisible {
		return menudebug.Plan{}, errors.New("topology captured, but this model/capability has no reviewed action profile; exact First Series two-bank A-active layout did not match")
	}
	bank, setup, home := menudebug.ScreenBank, menudebug.ScreenSetup, menudebug.ScreenHome
	capability := menudebug.CapabilityBank
	apply := []menudebug.Step{
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, FromFingerprint: screen.Fingerprint, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "A", ExpectedSelectionContains: "BNK B", ExpectedSaveVisible: true},
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeChangeValue, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "B", ExpectedSelectionContains: "BNK B", ExpectedSaveVisible: true},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "B", ExpectedSelection: "SAVE", ExpectedSaveVisible: true},
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeSave, ExpectedKind: home, ExpectedValue: "B", ExpectedStandbyHome: true, AllowStoringBeforeHome: true},
	}
	restore := []menudebug.Step{
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeEnterCandidate, ExpectedKind: setup, ExpectedSelection: "ANTENNA"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "CAT"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "MANUAL TUNE"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "DISPLAY"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelectionContains: "BEEP"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelectionContains: "START"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "TEMP/FANS"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "ALARMS LOG"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "TUN ANT"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "RX  ANT"},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: setup, ExpectedSelection: "BANK"},
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeEnterCandidate, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "B", ExpectedSelectionContains: "BNK B", ExpectedSaveVisible: true},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "B", ExpectedSelection: "SAVE", ExpectedSaveVisible: true},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "B", ExpectedSelectionContains: "BNK A", ExpectedSaveVisible: true},
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeChangeValue, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "A", ExpectedSelectionContains: "BNK A", ExpectedSaveVisible: true},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "A", ExpectedSelectionContains: "BNK B", ExpectedSaveVisible: true},
		{Action: menudebug.ActionRight, Purpose: menudebug.PurposeEnumerate, ExpectedKind: bank, ExpectedCapability: capability, ExpectedValue: "A", ExpectedSelection: "SAVE", ExpectedSaveVisible: true},
		{Action: menudebug.ActionSet, Purpose: menudebug.PurposeSave, ExpectedKind: home, ExpectedValue: "A", ExpectedStandbyHome: true, AllowStoringBeforeHome: true},
	}
	return menudebug.Plan{Profile: "expert-1.3k-fa-first-series-bank-ab-v1", ExpectedModel: "EXPERT 1.3K-FA", Capability: capability, OriginalValue: "A", CandidateValue: "B", Apply: apply, Restore: restore}, nil
}

func boolIs(value *bool, want bool) bool { return value != nil && *value == want }

var (
	reportIdentityPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()+-]{0,63}$`)
	firmwareVersionPattern = regexp.MustCompile(`(?i)^(?:unknown|(?:(?:fw|firmware)[ _-]*)?v?[0-9][A-Za-z0-9 ._()+-]{0,63})$`)
	hostnamePattern        = regexp.MustCompile(`(?i)(?:^|[ ._-])(?:[a-z0-9-]+\.)+[a-z]{2,63}(?:$|[ ._-])`)
	ipv4Pattern            = regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`)
	urlOrEmailPattern      = regexp.MustCompile(`(?i)(?:https?://|www\.|\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,})\S*`)
	devicePathPattern      = regexp.MustCompile(`(?i)(?:/dev/|/serial/|\\\\\.\\COM)\S*`)
	callsignPattern        = regexp.MustCompile(`\b[A-Z]{1,2}[0-9][A-Z]{1,4}(?:/[A-Z0-9]+)?\b`)
)

func validReportIdentity(value string) bool {
	value = strings.TrimSpace(value)
	return reportIdentityPattern.MatchString(value) && !ipv4Pattern.MatchString(value) && !callsignPattern.MatchString(strings.ToUpper(value))
}

func validFirmwareVersion(value string) bool {
	value = strings.TrimSpace(value)
	return firmwareVersionPattern.MatchString(value) && validReportIdentity(value) && !hostnamePattern.MatchString(value)
}

func sanitizeIdentity(value string) string {
	value = strings.TrimSpace(value)
	if !validReportIdentity(value) {
		return "unknown"
	}
	return value
}

func sanitizeModel(value string) string {
	value = sanitizeIdentity(value)
	if value == "unknown" || !strings.HasPrefix(strings.ToUpper(value), "EXPERT ") {
		return "unknown"
	}
	return value
}

func redactReportText(value string) string {
	value = ipv4Pattern.ReplaceAllString(value, "[REDACTED]")
	value = urlOrEmailPattern.ReplaceAllString(value, "[REDACTED]")
	value = devicePathPattern.ReplaceAllString(value, "[REDACTED]")
	value = callsignPattern.ReplaceAllString(value, "[REDACTED]")
	return value
}

func decodeStrictJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}
