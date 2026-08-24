package fanpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

type recordingButtons struct {
	actions []string
	err     error
	sent    bool
}

type leaseRecordingButtons struct {
	recordingButtons
	releases int
}

type sessionRecordingButtons struct {
	actions        []string
	authorizations []transport.SerialSessionWriteAuthorization
}

func (r *sessionRecordingButtons) SendButton(_ context.Context, action api.ButtonAction) (api.ActionResult, error) {
	return api.ActionResult{Name: action.Name}, errors.New("unbound button path used")
}

func (r *sessionRecordingButtons) SendButtonForSerialSession(_ context.Context, action api.ButtonAction, authorization transport.SerialSessionWriteAuthorization) (api.ActionResult, error) {
	r.actions = append(r.actions, action.Name)
	r.authorizations = append(r.authorizations, authorization)
	return api.ActionResult{Name: action.Name, Sent: true}, nil
}

func (r *leaseRecordingButtons) Acquire() bool      { return true }
func (r *leaseRecordingButtons) Release()           { r.releases++ }
func (r *leaseRecordingButtons) SafetyHold() bool   { return false }
func (r *leaseRecordingButtons) SetSafetyHold(bool) {}

func (r *recordingButtons) SendButton(_ context.Context, action api.ButtonAction) (api.ActionResult, error) {
	r.actions = append(r.actions, action.Name)
	if r.err != nil {
		return api.ActionResult{Name: action.Name}, r.err
	}
	if !r.sent {
		return api.ActionResult{Name: action.Name, Sent: true}, nil
	}
	return api.ActionResult{Name: action.Name, Sent: r.sent}, nil
}

func TestControllerRetainsDesiredPolicyAcrossHysteresis(t *testing.T) {
	controller := NewController()
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}

	if got := controller.Observe(statusAt(81, "operate", false), settings); got.DesiredPolicy != PolicyHigh {
		t.Fatalf("hot desired policy = %q", got.DesiredPolicy)
	}
	if got := controller.Observe(statusAt(77, "operate", false), settings); got.DesiredPolicy != PolicyHigh {
		t.Fatalf("hysteresis desired policy = %q", got.DesiredPolicy)
	}
	if got := controller.Observe(statusAt(74, "operate", false), settings); got.DesiredPolicy != PolicyNormal {
		t.Fatalf("cool desired policy = %q", got.DesiredPolicy)
	}
}

func TestControllerDisableClearsDesiredPolicy(t *testing.T) {
	controller := NewController()
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.Observe(statusAt(81, "standby", false), Settings{
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	})
	if got := controller.Current(); got.DesiredPolicy != PolicyUnknown || got.State != StateDisabled {
		t.Fatalf("unexpected disabled current result: %+v", got)
	}
}

func TestControllerViewUsesCurrentSettingsWithoutAdvancingHysteresis(t *testing.T) {
	controller := NewController(&recordingButtons{})
	controller.Observe(statusAt(81, "operate", false), Settings{
		Enabled:            true,
		DisplayProfile:     SupportedDisplayProfile,
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	})

	view := controller.View(statusAt(77, "operate", false), Settings{
		Enabled:            true,
		DisplayProfile:     SupportedDisplayProfile,
		HighTemperatureC:   90,
		NormalTemperatureC: 70,
	})
	if view.Thresholds.HighTemperatureC != 90 || view.Thresholds.NormalTemperatureC != 70 {
		t.Fatalf("view thresholds = %+v, want current settings", view.Thresholds)
	}
	if view.DesiredPolicy != PolicyHigh {
		t.Fatalf("view desired policy = %q, want retained high cooling", view.DesiredPolicy)
	}
	if controller.Current().Thresholds.HighTemperatureC != 80 {
		t.Fatalf("view mutated stored result: %+v", controller.Current())
	}
	if !view.ActionAvailable {
		t.Fatalf("view did not use supplied verified profile: %+v", view)
	}
}

func TestControllerNavigatesCapturedFirstSeriesProfileOneVerifiedStepAtATime(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "operate", false), settings)

	generation := uint64(1)
	observe := func(state display.State) Result {
		result := controller.ObserveDisplay(rxObservation(state, generation))
		t.Logf("generation=%d selected=%q state=%s nav=%+v", generation, selectedText(state), result.State, result.Navigation)
		generation++
		return result
	}
	observe(operateHomeScreen())
	controller.Observe(statusAt(81, "standby", false), settings)
	observe(homeScreen())
	for _, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP", "START", "TEMP/FANS"} {
		observe(setupScreenWithValues(selected, "On", "Oper"))
	}
	observe(submenuScreen("TEMPERATURE SCALE", "NORMAL"))
	observe(submenuScreen("FAN MANAGEMENT", "NORMAL"))
	observe(submenuScreen("FAN MANAGEMENT", "CONTEST"))
	observe(submenuScreen("SAVE", "CONTEST"))
	observe(storingScreen())
	observe(homeScreen())
	controller.Observe(statusAt(81, "operate", false), settings)
	result := observe(operateHomeScreen())

	want := []string{"operate", "set", "right", "right", "right", "right", "right", "right", "set", "right", "set", "right", "set", "operate"}
	if len(buttons.actions) != len(want) {
		t.Fatalf("actions = %v, want %v", buttons.actions, want)
	}
	for i := range want {
		if buttons.actions[i] != want[i] {
			t.Fatalf("actions = %v, want %v", buttons.actions, want)
		}
	}
	if result.State != StateSucceeded || result.CurrentPolicy != PolicyHigh || result.Pending {
		t.Fatalf("unexpected completed result: %+v", result)
	}
	if result.CurrentPolicyVerifiedAt == "" {
		t.Fatalf("completed result omitted verification timestamp: %+v", result)
	}
}

func TestControllerNavigatesCapturedSecondSeriesProfileOneVerifiedStepAtATime(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	status := statusAt(81, "standby", false)
	status.ModelName = "EXPERT 1.5K-FA"
	controller.Observe(status, settings)
	home := homeScreen()
	home.SetRow(1, "                       EXPERT 1.5K-FA")

	generation := uint64(1)
	observe := func(state display.State) Result {
		result := controller.ObserveDisplay(rxObservation(state, generation))
		t.Logf("generation=%d selected=%q state=%s nav=%+v", generation, selectedText(state), result.State, result.Navigation)
		generation++
		return result
	}
	observe(home)
	for _, selected := range []string{"CONFIG", "ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP", "START", "TEMP/FANS"} {
		observe(secondSeriesSetupScreen(selected))
	}
	observe(submenuScreen("TEMPERATURE SCALE", "NORMAL"))
	observe(submenuScreen("FAN MANAGEMENT", "NORMAL"))
	observe(submenuScreen("FAN MANAGEMENT", "CONTEST"))
	observe(submenuScreen("SAVE", "CONTEST"))
	observe(storingScreen())
	result := observe(home)

	want := []string{"set", "right", "right", "right", "right", "right", "right", "right", "set", "right", "set", "right", "set"}
	if strings.Join(buttons.actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", buttons.actions, want)
	}
	if result.State != StateSucceeded || result.CurrentPolicy != PolicyHigh || result.Pending || result.Navigation.Profile != SecondSeriesDisplayProfile {
		t.Fatalf("unexpected completed result: %+v", result)
	}
}

func TestControllerRestoresNormalWithCapturedSecondSeriesProfile(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{HighTemperatureC: 80, NormalTemperatureC: 75}
	if err := controller.SetManualOverride(PolicyNormal, 0); err != nil {
		t.Fatal(err)
	}
	status := statusAt(81, "standby", false)
	status.ModelName = "EXPERT 1.5K-FA"
	controller.Observe(status, settings)
	home := homeScreen()
	home.SetRow(1, "                       EXPERT 1.5K-FA")

	generation := uint64(1)
	observe := func(state display.State) Result {
		result := controller.ObserveDisplay(rxObservation(state, generation))
		generation++
		return result
	}
	observe(home)
	for _, selected := range []string{"CONFIG", "ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP", "START", "TEMP/FANS"} {
		observe(secondSeriesSetupScreen(selected))
	}
	observe(submenuScreen("TEMPERATURE SCALE", "CONTEST"))
	observe(submenuScreen("FAN MANAGEMENT", "CONTEST"))
	observe(submenuScreen("FAN MANAGEMENT", "NORMAL"))
	observe(submenuScreen("SAVE", "NORMAL"))
	observe(storingScreen())
	result := observe(home)

	want := []string{"set", "right", "right", "right", "right", "right", "right", "right", "set", "right", "set", "right", "set"}
	if strings.Join(buttons.actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", buttons.actions, want)
	}
	if result.State != StateSucceeded || result.CurrentPolicy != PolicyNormal || result.Pending || result.Navigation.Profile != SecondSeriesDisplayProfile {
		t.Fatalf("unexpected completed result: %+v", result)
	}
}

func TestControllerRejectsModelAndInitialSetupTopologyCrossPairs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		model      string
		setupState display.State
	}{
		{name: "1.3 model with same-label second-series setup", model: "EXPERT 1.3K-FA", setupState: secondSeriesSetupScreen("ANTENNA")},
		{name: "1.5 model with same-label first-series setup", model: "EXPERT 1.5K-FA", setupState: setupScreen("ANTENNA")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buttons := &recordingButtons{}
			controller := NewController(buttons)
			settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
			status := statusAt(81, "standby", false)
			status.ModelName = tc.model
			controller.Observe(status, settings)
			home := homeScreen()
			home.SetRow(1, "                       "+tc.model)
			controller.ObserveDisplay(rxObservation(home, 1))
			result := controller.ObserveDisplay(rxObservation(tc.setupState, 2))
			if result.State != StateFailed || len(buttons.actions) != 1 || buttons.actions[0] != "set" || !result.Navigation.MayBeInMenu {
				t.Fatalf("cross-pair did not fail before selector movement: result=%+v actions=%v", result, buttons.actions)
			}
		})
	}
}

func TestControllerRejectsWrongSetupFamilyAfterProfileSelection(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 2))
	result := controller.ObserveDisplay(rxObservation(secondSeriesSetupScreen("CAT"), 3))
	if result.State != StateFailed || len(buttons.actions) != 2 || strings.Join(buttons.actions, ",") != "set,right" {
		t.Fatalf("wrong setup family advanced navigation: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerRejectsFirstSeriesSetupDuringSecondSeriesNavigation(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	status := statusAt(81, "standby", false)
	status.ModelName = "EXPERT 1.5K-FA"
	controller.Observe(status, settings)
	home := homeScreen()
	home.SetRow(1, "                       EXPERT 1.5K-FA")
	controller.ObserveDisplay(rxObservation(home, 1))
	controller.ObserveDisplay(rxObservation(secondSeriesSetupScreen("CONFIG"), 2))
	result := controller.ObserveDisplay(rxObservation(setupScreen("CAT"), 3))
	if result.State != StateFailed || strings.Join(buttons.actions, ",") != "set,right" || !result.Navigation.MayBeInMenu {
		t.Fatalf("wrong setup family advanced Second Series navigation: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerFailsClosedWhenModelChangesDuringSecondSeriesNavigation(t *testing.T) {
	for _, changedModel := range []string{"EXPERT 1.3K-FA", "EXPERT 2K-FA"} {
		t.Run(changedModel, func(t *testing.T) {
			buttons := &recordingButtons{}
			controller := NewController(buttons)
			settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
			status := statusAt(81, "standby", false)
			status.ModelName = "EXPERT 1.5K-FA"
			controller.Observe(status, settings)
			home := homeScreen()
			home.SetRow(1, "                       EXPERT 1.5K-FA")
			controller.ObserveDisplay(rxObservation(home, 1))
			if strings.Join(buttons.actions, ",") != "set" {
				t.Fatalf("initial actions = %v", buttons.actions)
			}

			status.ModelName = changedModel
			result := controller.Observe(status, settings)
			if result.State != StateFailed || strings.Join(buttons.actions, ",") != "set" || !result.Navigation.MayBeInMenu {
				t.Fatalf("model change did not fail closed before another write: result=%+v actions=%v", result, buttons.actions)
			}
		})
	}
}

func TestControllerFailsClosedWhenFirstSeriesChangesToSecondSeries(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	status := statusAt(81, "standby", false)
	controller.Observe(status, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if strings.Join(buttons.actions, ",") != "set" {
		t.Fatalf("initial actions = %v", buttons.actions)
	}

	status.ModelName = "EXPERT 1.5K-FA"
	result := controller.Observe(status, settings)
	if result.State != StateFailed || strings.Join(buttons.actions, ",") != "set" || !result.Navigation.MayBeInMenu {
		t.Fatalf("supported model change did not fail closed before another write: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerUsesAtomicSerialSessionAuthorizationForMenuAndOperateWrites(t *testing.T) {
	for _, tc := range []struct {
		name          string
		statusState   string
		display       display.State
		wantAction    string
		wantOperating string
	}{
		{name: "standby menu entry", statusState: "standby", display: homeScreen(), wantAction: "set", wantOperating: "standby"},
		{name: "operate transition", statusState: "operate", display: operateHomeScreen(), wantAction: "operate", wantOperating: "operate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buttons := &sessionRecordingButtons{}
			controller := NewController(buttons)
			controller.ObserveSerialSession(7)
			settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
			status := statusAt(81, tc.statusState, false)
			controller.ObserveFromSerialSession(status, settings, 7)
			observation := rxObservation(tc.display, 1)
			observation.SerialSessionGeneration = 7
			controller.ObserveDisplay(observation)
			if strings.Join(buttons.actions, ",") != tc.wantAction || len(buttons.authorizations) != 1 {
				t.Fatalf("actions=%v authorizations=%+v", buttons.actions, buttons.authorizations)
			}
			authorization := buttons.authorizations[0]
			if authorization.SessionGeneration != 7 || authorization.Model != "EXPERT 1.3K-FA" || authorization.ExpectedOperatingState != tc.wantOperating {
				t.Fatalf("authorization=%+v", authorization)
			}
		})
	}
}

func TestControllerPreservesNonSessionCoordinatorTransport(t *testing.T) {
	buttons := &recordingButtons{}
	coordinator := transport.NewActuationCoordinator(buttons)
	controller := NewController(coordinator.Owner(transport.ActuationOwnerFan, false))
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	result := controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if strings.Join(buttons.actions, ",") != "set" || result.State != StateNavigating {
		t.Fatalf("coordinator-wrapped non-session transport was blocked: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerRequiresStatusAndDisplayFromBoundSerialSession(t *testing.T) {
	buttons := &sessionRecordingButtons{}
	controller := NewController(buttons)
	controller.ObserveSerialSession(7)
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.ObserveFromSerialSession(statusAt(81, "standby", false), settings, 7)
	observation := rxObservation(homeScreen(), 1)
	observation.SerialSessionGeneration = 8
	result := controller.ObserveDisplay(observation)
	if len(buttons.actions) != 0 || result.State != StateBlocked || controller.lastDisplayGen != 0 {
		t.Fatalf("mismatched display session authorized navigation: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerIgnoresStaleSerialSessionNotification(t *testing.T) {
	controller := NewController()
	controller.ObserveSerialSession(8)
	controller.ObserveSerialSession(7)
	if controller.serialSessionGeneration != 8 {
		t.Fatalf("serial session rewound to %d", controller.serialSessionGeneration)
	}
}

func TestControllerIgnoresStatusFromRetiredSerialSession(t *testing.T) {
	controller := NewController()
	settings := Settings{}
	controller.ObserveSerialSession(8)
	current := statusAt(30, "standby", false)
	current.ModelName = "EXPERT 1.5K-FA"
	controller.ObserveFromSerialSession(current, settings, 8)
	wantGeneration := controller.statusGeneration

	stale := statusAt(90, "operate", true)
	stale.ModelName = "EXPERT 1.3K-FA"
	controller.ObserveFromSerialSession(stale, settings, 7)
	if controller.statusGeneration != wantGeneration || controller.statusSerialSessionGeneration != 8 ||
		controller.status.ModelName != "EXPERT 1.5K-FA" || controller.status.OperatingState != "standby" {
		t.Fatalf("retired status replaced current evidence: generation=%d session=%d status=%+v", controller.statusGeneration, controller.statusSerialSessionGeneration, controller.status)
	}
}

func TestControllerIgnoresDisplaysFromRetiredSerialSession(t *testing.T) {
	controller := NewController()
	disabled := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	status := statusAt(30, "standby", false)
	controller.ObserveSerialSession(8)
	controller.ObserveFromSerialSession(status, disabled, 8)
	controller.nav = navState{failed: true, failureAfterGen: 0}
	controller.mayBeInMenu = true

	for generation, state := range []display.State{
		submenuScreen("FAN MANAGEMENT", "CONTEST"),
		submenuScreen("SAVE", "CONTEST"),
		storingScreen(),
		homeScreen(),
	} {
		observation := rxObservation(state, uint64(generation+1))
		observation.SerialSessionGeneration = 7
		controller.ObserveDisplay(observation)
	}
	if controller.lastDisplayGen != 0 || controller.passive != (passiveSaveState{}) || controller.current != PolicyUnknown {
		t.Fatalf("retired display mutated evidence: display=%d passive=%+v current=%q", controller.lastDisplayGen, controller.passive, controller.current)
	}
	if err := controller.Recover(status, disabled); err == nil || !strings.Contains(err.Error(), "newer checksum-valid home") {
		t.Fatalf("retired home display authorized recovery: %v", err)
	}
}

func TestControllerClearsVerifiedReceiptWhenSupportedModelChanges(t *testing.T) {
	controller := NewController()
	controller.current = PolicyHigh
	controller.currentConfidence = "verified-live"
	controller.currentVerifiedAt = time.Now().Add(-time.Minute)
	controller.currentModel = "EXPERT 1.5K-FA"
	status := statusAt(81, "standby", false)
	status.ModelName = "EXPERT 1.3K-FA"
	result := controller.Observe(status, Settings{})
	if result.CurrentPolicy != PolicyUnknown || result.CurrentPolicyConfidence != "unknown" || controller.currentModel != "" {
		t.Fatalf("replacement model inherited verified receipt: %+v", result)
	}
}

func TestControllerBlocksUnreviewedModelBeforeEnteringSetup(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75}
	status := statusAt(81, "standby", false)
	status.ModelName = "EXPERT F-KFA"
	controller.Observe(status, settings)
	home := homeScreen()
	home.SetRow(1, "                       EXPERT F-KFA")
	result := controller.ObserveDisplay(rxObservation(home, 1))
	if result.State != StateBlocked || result.ActionAvailable || len(buttons.actions) != 0 || !containsBlock(result.BlockedBy, "supported-fan-profile") {
		t.Fatalf("unreviewed model was not blocked before SET: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerAcceptsNewerHomeWhenStoringFrameIsMissed(t *testing.T) {
	controller := NewController(&recordingButtons{})
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	driveToFanManagement(controller, settings, PolicyNormal)
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", "NORMAL"), 20))
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", "CONTEST"), 21))
	controller.ObserveDisplay(rxObservation(submenuScreen("SAVE", "CONTEST"), 22))
	result := controller.ObserveDisplay(rxObservation(homeScreen(), 23))

	if result.State != StateSucceeded || result.CurrentPolicy != PolicyHigh ||
		result.CurrentPolicyConfidence != "verified-live" || result.Navigation.MayBeInMenu {
		t.Fatalf("newer verified home did not complete SAVE after missed STORING frame: %+v", result)
	}
}

func TestControllerAcceptsCapturedSetupValueVariants(t *testing.T) {
	for _, beep := range []string{"On", "Off"} {
		for _, start := range []string{"Stby", "Oper"} {
			t.Run(beep+"/"+start, func(t *testing.T) {
				buttons := &recordingButtons{}
				controller := NewController(buttons)
				settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
				controller.Observe(statusAt(81, "standby", false), settings)

				generation := uint64(1)
				observe := func(state display.State) Result {
					result := controller.ObserveDisplay(rxObservation(state, generation))
					generation++
					return result
				}
				observe(homeScreen())
				for _, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP", "START", "TEMP/FANS"} {
					observe(setupScreenWithValues(selected, beep, start))
				}
				observe(submenuScreen("TEMPERATURE SCALE", "NORMAL"))
				observe(submenuScreen("FAN MANAGEMENT", "NORMAL"))
				observe(submenuScreen("FAN MANAGEMENT", "CONTEST"))
				observe(submenuScreen("SAVE", "CONTEST"))
				observe(storingScreen())
				result := observe(homeScreen())
				if result.State != StateSucceeded || result.CurrentPolicy != PolicyHigh {
					t.Fatalf("variant failed: %+v actions=%v", result, buttons.actions)
				}
			})
		}
	}
}

func TestStandbyHomeAcceptsOtherExpertModels(t *testing.T) {
	state := homeScreen()
	state.SetRow(1, "                       EXPERT 2K-FA")
	if !matchesStandbyHome(state) {
		t.Fatal("2K-FA standby home was rejected before experimental menu verification")
	}
}

func TestControllerRejectsUncapturedSetupValues(t *testing.T) {
	for _, state := range []display.State{
		setupScreenWithValues("BEEP", "Auto", "Stby"),
		setupScreenWithValues("START", "On", "Last"),
	} {
		if key := semanticDisplayKey(state); key != "" {
			t.Fatalf("uncaptured setup screen matched semantic key %q", key)
		}
	}
}

func TestSecondSeriesSetupProfileRequiresExactLayoutAndHighlightGeometry(t *testing.T) {
	state := secondSeriesSetupScreen("CONFIG")
	key, ok := secondSeriesSetupSelectionKey(state)
	if !ok || key != "setup:CONFIG" {
		t.Fatalf("captured Second Series setup was not recognized: key=%q ok=%v", key, ok)
	}
	input2 := state
	input2.SetRow(0, "       SETUP OPTIONS vs. INPUT 2")
	if key, ok := secondSeriesSetupSelectionKey(input2); !ok || key != "setup:CONFIG" {
		t.Fatalf("Second Series Input 2 setup was not recognized: key=%q ok=%v", key, ok)
	}
	unknownInput := state
	unknownInput.SetRow(0, "       SETUP OPTIONS vs. INPUT X")
	if key, ok := secondSeriesSetupSelectionKey(unknownInput); ok {
		t.Fatalf("unknown Second Series input matched key %q", key)
	}
	for _, mutate := range []func(*display.State){
		func(s *display.State) { s.SetRow(1, " CONFIG       DISPLAY       STATUS") },
		func(s *display.State) { s.SetRow(4, " MANUAL TUNE  TEMP/FANS     BANK") },
		func(s *display.State) {
			for col := 0; col < display.Cols; col++ {
				s.SetAttr(1, col, 0)
			}
			highlight(s, 1, 1, 13)
		},
	} {
		nearMatch := state
		mutate(&nearMatch)
		if _, ok := secondSeriesSetupSelectionKey(nearMatch); ok {
			t.Fatal("near-match Second Series setup selected a profile")
		}
	}
	if _, _, ok := identifyFanDisplayProfile(state, "EXPERT 1.3K-FA", ""); ok {
		t.Fatal("Second Series topology matched the First Series model")
	}
}

func TestThirdSeriesProductionUsesExactCapturedNormalQuietPath(t *testing.T) {
	buttons := &leaseRecordingButtons{}
	controller := NewController(buttons)
	if err := controller.SetManualOverride(PolicyNormal, 0); err != nil {
		t.Fatal(err)
	}
	settings := Settings{HighTemperatureC: 80, NormalTemperatureC: 75, FirmwareVersion: ThirdSeriesFirmware}
	status := statusAt(70, "standby", false)
	status.ModelName = "EXPERT 2K-FA"
	controller.Observe(status, settings)

	files := []string{
		"report_00acd527_00_g367_home.state.json",
		"report_00acd527_01_g368_setup_menu_config.state.json",
		"report_00acd527_02_g369_setup_menu_antenna.state.json",
		"report_00acd527_03_g371_setup_menu_cat.state.json",
		"report_00acd527_04_g372_setup_menu_manual_tune.state.json",
		"report_00acd527_05_g374_setup_menu_display.state.json",
		"report_00acd527_06_g375_setup_menu_beep_on.state.json",
		"report_00acd527_07_g377_setup_menu_start_oprt.state.json",
		"report_00acd527_08_g379_setup_menu_temp_f.state.json",
		"report_00acd527_09_g380_setup_menu_alarms_log.state.json",
		"report_00acd527_10_g381_setup_menu_tun_ant.state.json",
		"report_00acd527_11_g383_setup_menu_rx_ant.state.json",
		"report_00acd527_12_g384_setup_menu_fan_noise.state.json",
		"report_00acd527_13_g385_fan_menu_normal_mode_all_modes.state.json",
		"report_00acd527_14_g387_fan_menu_save.state.json",
		"report_00acd527_15_g388_fan_menu_quiet_mode_ssb_only.state.json",
		"report_00acd527_16_g389_fan_menu_quiet_mode_ssb_only.state.json",
		"report_00acd527_17_g391_fan_menu_normal_mode_all_modes.state.json",
		"report_00acd527_18_g392_fan_menu_save.state.json",
		"report_00acd527_19_g394_storing.state.json",
		"report_00acd527_00_g367_home.state.json",
	}
	var result Result
	for generation, name := range files {
		state := loadThirdSeriesReportState(t, name)
		result = controller.ObserveDisplay(DisplayObservation{State: state, Generation: uint64(generation + 1), TX: boolPtr(false), Operate: boolPtr(false)})
	}
	want := []string{"set", "right", "right", "right", "right", "right", "right", "right", "right", "right", "right", "right", "set", "right", "right", "set", "right", "right", "set"}
	if strings.Join(buttons.actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions=%v want=%v", buttons.actions, want)
	}
	if result.State != StateSucceeded || result.CurrentPolicy != PolicyNormal || result.Navigation.Profile != ThirdSeriesDisplayProfile {
		t.Fatalf("result=%+v", result)
	}
	for _, action := range buttons.actions {
		if action == "display" || action == "operate" {
			t.Fatalf("forbidden Third Series action %q in %v", action, buttons.actions)
		}
	}
}

func TestThirdSeriesProductionMapsContestRequestToHardwareNormal(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	if err := controller.SetManualOverride(PolicyHigh, 0); err != nil {
		t.Fatal(err)
	}
	settings := Settings{HighTemperatureC: 80, NormalTemperatureC: 75, FirmwareVersion: ThirdSeriesFirmware}
	status := statusAt(70, "standby", false)
	status.ModelName = "EXPERT 2K-FA"
	controller.Observe(status, settings)
	files := []string{
		"report_00acd527_00_g367_home.state.json", "report_00acd527_01_g368_setup_menu_config.state.json",
		"report_00acd527_02_g369_setup_menu_antenna.state.json", "report_00acd527_03_g371_setup_menu_cat.state.json",
		"report_00acd527_04_g372_setup_menu_manual_tune.state.json", "report_00acd527_05_g374_setup_menu_display.state.json",
		"report_00acd527_06_g375_setup_menu_beep_on.state.json", "report_00acd527_07_g377_setup_menu_start_oprt.state.json",
		"report_00acd527_08_g379_setup_menu_temp_f.state.json", "report_00acd527_09_g380_setup_menu_alarms_log.state.json",
		"report_00acd527_10_g381_setup_menu_tun_ant.state.json", "report_00acd527_11_g383_setup_menu_rx_ant.state.json",
		"report_00acd527_12_g384_setup_menu_fan_noise.state.json", "report_00acd527_16_g389_fan_menu_quiet_mode_ssb_only.state.json",
		"report_00acd527_17_g391_fan_menu_normal_mode_all_modes.state.json", "report_00acd527_13_g385_fan_menu_normal_mode_all_modes.state.json",
		"report_00acd527_14_g387_fan_menu_save.state.json", "report_00acd527_22_g2378_storing.state.json",
		"report_00acd527_00_g367_home.state.json",
	}
	var result Result
	for generation, name := range files {
		result = controller.ObserveDisplay(DisplayObservation{State: loadThirdSeriesReportState(t, name), Generation: uint64(generation + 1), TX: boolPtr(false), Operate: boolPtr(false)})
	}
	for _, action := range buttons.actions {
		if action == "display" || action == "operate" {
			t.Fatalf("forbidden action %q", action)
		}
	}
	if result.State != StateSucceeded || result.CurrentPolicy != PolicyHigh {
		t.Fatalf("result=%+v actions=%v", result, buttons.actions)
	}
}

func TestThirdSeriesProductionRequiresExactFirmwareAndStandbyBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name, firmware, operating string
		blocked                   string
	}{
		{"missing firmware", "", "standby", "supported-fan-profile"},
		{"wrong firmware", "Rel.26_03_24_B", "standby", "supported-fan-profile"},
		{"operate", ThirdSeriesFirmware, "operate", "standby-only-profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buttons := &recordingButtons{}
			controller := NewController(buttons)
			settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75, FirmwareVersion: tc.firmware}
			status := statusAt(81, tc.operating, false)
			status.ModelName = "EXPERT 2K-FA"
			result := controller.Observe(status, settings)
			if !containsBlock(result.BlockedBy, tc.blocked) {
				t.Fatalf("result=%+v", result)
			}
			controller.ObserveDisplay(DisplayObservation{State: loadThirdSeriesReportState(t, "report_00acd527_00_g367_home.state.json"), Generation: 1, TX: boolPtr(false), Operate: boolPtr(false)})
			if len(buttons.actions) != 0 {
				t.Fatalf("blocked profile wrote %v", buttons.actions)
			}
		})
	}
}

func TestThirdSeriesCapturedFanMapping(t *testing.T) {
	selected, policy, ok := ThirdSeriesFanScreen(loadThirdSeriesReportState(t, "report_00acd527_13_g385_fan_menu_normal_mode_all_modes.state.json"))
	if !ok || selected != "hardware-normal" || policy != PolicyHigh {
		t.Fatalf("NORMAL frame selected=%q policy=%q ok=%v", selected, policy, ok)
	}
	selected, policy, ok = ThirdSeriesFanScreen(loadThirdSeriesReportState(t, "report_00acd527_16_g389_fan_menu_quiet_mode_ssb_only.state.json"))
	if !ok || selected != "quiet" || policy != PolicyNormal {
		t.Fatalf("QUIET frame selected=%q policy=%q ok=%v", selected, policy, ok)
	}
}

func TestThirdSeriesRejectsAmbiguousActiveMarker(t *testing.T) {
	base := loadThirdSeriesReportState(t, "report_00acd527_13_g385_fan_menu_normal_mode_all_modes.state.json")
	for _, tc := range []struct {
		name          string
		quiet, normal byte
	}{
		{"missing", 0x60, 0x60},
		{"dual", 0xae, 0xae},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := base
			state.Chars[2][4] = tc.quiet
			state.Chars[3][4] = tc.normal
			if _, _, ok := ThirdSeriesFanScreen(state); ok {
				t.Fatal("ambiguous marker accepted")
			}
		})
	}
}

func TestThirdSeriesTXAndStaleReceiptsDoNotAuthorizeFollowup(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, HighTemperatureC: 80, NormalTemperatureC: 75, FirmwareVersion: ThirdSeriesFirmware}
	status := statusAt(81, "standby", true)
	status.ModelName = "EXPERT 2K-FA"
	result := controller.Observe(status, settings)
	if !containsBlock(result.BlockedBy, "rx") {
		t.Fatalf("TX result=%+v", result)
	}
	controller.ObserveDisplay(DisplayObservation{State: loadThirdSeriesReportState(t, "report_00acd527_00_g367_home.state.json"), Generation: 1, TX: boolPtr(true), Operate: boolPtr(false)})
	if len(buttons.actions) != 0 {
		t.Fatalf("TX wrote %v", buttons.actions)
	}

	status = statusAt(81, "standby", false)
	status.ModelName = "EXPERT 2K-FA"
	controller.Observe(status, settings)
	controller.ObserveDisplay(DisplayObservation{State: loadThirdSeriesReportState(t, "report_00acd527_00_g367_home.state.json"), Generation: 2, TX: boolPtr(false), Operate: boolPtr(false)})
	if strings.Join(buttons.actions, ",") != "set" {
		t.Fatalf("home actions=%v", buttons.actions)
	}
	controller.ObserveDisplay(DisplayObservation{State: loadThirdSeriesReportState(t, "report_00acd527_01_g368_setup_menu_config.state.json"), Generation: 2, TX: boolPtr(false), Operate: boolPtr(false)})
	if strings.Join(buttons.actions, ",") != "set" {
		t.Fatalf("stale receipt wrote follow-up: %v", buttons.actions)
	}
	controller.Tick(time.Now().Add(navigationTimeout + time.Second))
	if strings.Join(buttons.actions, ",") != "set" {
		t.Fatalf("timeout sent blind recovery: %v", buttons.actions)
	}
	if !controller.Current().Navigation.MayBeInMenu || controller.Current().State != StateFailed {
		t.Fatalf("timeout did not fail closed: %+v", controller.Current())
	}
}

func loadThirdSeriesReportState(t *testing.T, name string) display.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "server", "testdata", "third_series", name))
	if err != nil {
		t.Fatal(err)
	}
	var state display.State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func boolPtr(value bool) *bool { return &value }

func TestControllerIgnoresLiveHomeTelemetryChangesWhileWaitingForSetup(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreenAt("26 C"), 1))
	result := controller.ObserveDisplay(rxObservation(homeScreenAt("28 C"), 2))
	if result.State != StateNavigating || len(buttons.actions) != 1 || !result.Navigation.MayBeInMenu {
		t.Fatalf("live home telemetry caused a write or failure: result=%+v actions=%v", result, buttons.actions)
	}
	result = controller.Tick(time.Now().Add(navigationTimeout + time.Second))
	if result.State != StateFailed || !result.Navigation.MayBeInMenu || result.Navigation.RecoveryState != "operator-required" {
		t.Fatalf("delayed home cleared recovery uncertainty: %+v", result)
	}
}

func TestControllerIgnoresDuplicateFrameThenFailsClosedOnUnexpectedNewScreen(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	home := homeScreen()
	controller.ObserveDisplay(rxObservation(home, 1))
	controller.ObserveDisplay(rxObservation(home, 2))
	if len(buttons.actions) != 1 {
		t.Fatalf("duplicate home caused another write: %v", buttons.actions)
	}
	result := controller.ObserveDisplay(rxObservation(setupScreen("CAT"), 3))
	if result.State != StateFailed || result.Navigation.LastError == "" {
		t.Fatalf("unexpected mismatch result: %+v", result)
	}
	controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 4))
	if len(buttons.actions) != 1 {
		t.Fatalf("failed controller retried: %v", buttons.actions)
	}
}

func TestControllerClearsStaleHysteresisOnContactLoss(t *testing.T) {
	controller := NewController()
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	stale := statusAt(77, "standby", false)
	stale.RecentContact = false
	controller.Observe(stale, settings)
	result := controller.Observe(statusAt(77, "standby", false), settings)
	if result.DesiredPolicy != PolicyUnknown || result.Pending {
		t.Fatalf("stale desired policy survived contact loss: %+v", result)
	}
}

func TestControllerNavigationTimeoutLatchesWithoutRetry(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	result := controller.Tick(time.Now().Add(navigationTimeout + time.Second))
	if result.State != StateFailed || len(buttons.actions) != 1 {
		t.Fatalf("unexpected timeout result: %+v actions=%v", result, buttons.actions)
	}
}

func TestControllerDoesNotNavigateWithoutKnownDesiredPolicy(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	controller.Observe(statusAt(77, "standby", false), Settings{
		Enabled:            true,
		DisplayProfile:     SupportedDisplayProfile,
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	})

	result := controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if len(buttons.actions) != 0 || result.State != StateNormal || result.DesiredPolicy != PolicyUnknown {
		t.Fatalf("unknown hysteresis decision navigated: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerSavesMatchingPolicyWithoutToggling(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	controller.normalRestoreAfter = time.Time{}
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(74, "standby", false), settings)

	generation := uint64(1)
	observe := func(state display.State) {
		controller.ObserveDisplay(rxObservation(state, generation))
		generation++
	}
	observe(homeScreen())
	for _, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP    On", "START   Stby", "TEMP/FANS"} {
		observe(setupScreen(selected))
	}
	observe(submenuScreen("TEMPERATURE SCALE", "NORMAL"))
	observe(submenuScreen("FAN MANAGEMENT", "NORMAL"))
	observe(submenuScreen("SAVE", "NORMAL"))
	observe(storingScreen())
	observe(homeScreen())

	want := []string{"set", "right", "right", "right", "right", "right", "right", "set", "right", "right", "set"}
	if len(buttons.actions) != len(want) {
		t.Fatalf("actions = %v, want %v", buttons.actions, want)
	}
	for i := range want {
		if buttons.actions[i] != want[i] {
			t.Fatalf("actions = %v, want %v", buttons.actions, want)
		}
	}
}

func TestControllerFailsClosedIfOperateReturnsDuringMenuNavigation(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))

	controller.Observe(statusAt(81, "operate", false), settings)
	result := controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 2))
	if result.State != StateFailed || len(buttons.actions) != 1 {
		t.Fatalf("unexpected OPERATE did not fail closed: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerFailsClosedWhenOperatingStateBecomesUnknown(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))

	controller.Observe(statusAt(81, "unknown", false), settings)
	result := controller.Current()
	if result.State != StateFailed || len(buttons.actions) != 1 {
		t.Fatalf("unknown operating state did not fail closed: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerPausesForProtocolTXAndResumesOnlyAfterOrderedFreshRXEvidence(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}

	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if len(buttons.actions) != 1 {
		t.Fatalf("navigation did not start from STANDBY/RX home: %v", buttons.actions)
	}

	controller.Observe(statusAt(81, "standby", true), settings)
	result := controller.ObserveDisplay(txObservation(setupScreen("ANTENNA"), 2))
	if result.State != StatePaused || !result.Navigation.Paused || len(buttons.actions) != 1 {
		t.Fatalf("TX did not pause writes: result=%+v actions=%v", result, buttons.actions)
	}
	now = now.Add(15 * time.Second)
	if result = controller.Tick(now); result.State != StatePaused || len(buttons.actions) != 1 {
		t.Fatalf("FT8-length TX tripped timeout or wrote: result=%+v actions=%v", result, buttons.actions)
	}

	// LCD RX before protocol RX is not enough.
	result = controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 3))
	if result.State != StatePaused || len(buttons.actions) != 1 {
		t.Fatalf("pre-status LCD RX resumed writes: result=%+v actions=%v", result, buttons.actions)
	}
	controller.Observe(statusAt(81, "standby", false), settings)
	// Fresh protocol RX still requires a newer LCD generation.
	if result = controller.Current(); result.State != StatePaused || len(buttons.actions) != 1 {
		t.Fatalf("protocol RX alone resumed writes: result=%+v actions=%v", result, buttons.actions)
	}
	result = controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 4))
	if result.State != StateNavigating || result.Navigation.Paused || len(buttons.actions) != 2 || buttons.actions[1] != "right" {
		t.Fatalf("ordered RX evidence did not resume one verified step: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerLCDTXPausesBeforeProtocolTXAndRequiresPostPauseStatusRX(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}

	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	result := controller.ObserveDisplay(txObservation(setupScreen("ANTENNA"), 2))
	if result.State != StatePaused || len(buttons.actions) != 1 {
		t.Fatalf("LCD TX did not pause before protocol TX: result=%+v actions=%v", result, buttons.actions)
	}
	result = controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 3))
	if result.State != StatePaused || len(buttons.actions) != 1 {
		t.Fatalf("cached pre-TX protocol RX incorrectly resumed writes: result=%+v actions=%v", result, buttons.actions)
	}
	controller.Observe(statusAt(81, "standby", false), settings)
	result = controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 4))
	if result.State != StateNavigating || len(buttons.actions) != 2 {
		t.Fatalf("post-pause status and LCD RX did not resume: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerResumesStandbyLCDVerificationAfterTX(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}

	controller.Observe(statusAt(81, "operate", false), settings)
	controller.ObserveDisplay(rxObservation(operateHomeScreen(), 1))
	controller.Observe(statusAt(81, "standby", false), settings)

	controller.Observe(statusAt(81, "standby", true), settings)
	result := controller.ObserveDisplay(txObservation(homeScreen(), 2))
	if result.State != StatePaused || len(buttons.actions) != 1 {
		t.Fatalf("TX did not pause STANDBY LCD verification: result=%+v actions=%v", result, buttons.actions)
	}
	result = controller.ObserveDisplay(rxObservation(homeScreen(), 3))
	if result.State != StatePaused || len(buttons.actions) != 1 {
		t.Fatalf("LCD RX before protocol RX resumed STANDBY verification: result=%+v actions=%v", result, buttons.actions)
	}
	controller.Observe(statusAt(81, "standby", false), settings)
	result = controller.ObserveDisplay(rxObservation(homeScreen(), 4))
	if result.State != StateNavigating || result.Navigation.Paused || len(buttons.actions) != 2 || buttons.actions[1] != "set" {
		t.Fatalf("ordered RX evidence did not resume STANDBY verification: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerUnknownProtocolTXFailsClosedAfterPause(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}

	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	controller.ObserveDisplay(txObservation(setupScreen("ANTENNA"), 2))

	unknown := statusAt(81, "standby", false)
	unknown.TX = nil
	controller.Observe(unknown, settings)
	result := controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 3))
	if result.State != StateFailed || len(buttons.actions) != 1 {
		t.Fatalf("unknown protocol TX/RX did not fail closed: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerPostTXResumeFailsOnWrongScreenAndTimesOutWithoutLCDRX(t *testing.T) {
	t.Run("wrong screen", func(t *testing.T) {
		buttons := &recordingButtons{}
		controller := NewController(buttons)
		settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
		controller.Observe(statusAt(81, "standby", false), settings)
		controller.ObserveDisplay(rxObservation(homeScreen(), 1))
		controller.ObserveDisplay(txObservation(setupScreen("ANTENNA"), 2))
		controller.Observe(statusAt(81, "standby", false), settings)
		result := controller.ObserveDisplay(rxObservation(setupScreen("CAT"), 3))
		if result.State != StateFailed || len(buttons.actions) != 1 {
			t.Fatalf("wrong post-TX waypoint did not fail closed: result=%+v actions=%v", result, buttons.actions)
		}
	})

	t.Run("missing LCD RX", func(t *testing.T) {
		buttons := &recordingButtons{}
		controller := NewController(buttons)
		now := time.Date(2026, 7, 30, 13, 30, 0, 0, time.UTC)
		controller.now = func() time.Time { return now }
		settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
		controller.Observe(statusAt(81, "standby", false), settings)
		controller.ObserveDisplay(rxObservation(homeScreen(), 1))
		controller.ObserveDisplay(txObservation(setupScreen("ANTENNA"), 2))
		controller.Observe(statusAt(81, "standby", false), settings)
		now = now.Add(navigationTimeout / 2)
		controller.Observe(statusAt(81, "standby", false), settings)
		now = now.Add(navigationTimeout / 2)
		controller.Observe(statusAt(81, "standby", false), settings)
		now = now.Add(navigationTimeout + time.Second)
		result := controller.Tick(now)
		if result.State != StateFailed || len(buttons.actions) != 1 {
			t.Fatalf("missing post-TX LCD RX did not time out: result=%+v actions=%v", result, buttons.actions)
		}
	})
}

func TestControllerCachedSettingsUpdateCannotSatisfyPostPauseRXBarrier(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	rx := statusAt(81, "standby", false)

	controller.Observe(rx, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	controller.ObserveDisplay(txObservation(setupScreen("ANTENNA"), 2))
	controller.UpdateSettings(rx, settings)
	result := controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 3))
	if result.State != StatePaused || !result.Navigation.Paused || len(buttons.actions) != 1 {
		t.Fatalf("cached settings status satisfied the protocol RX barrier: result=%+v actions=%v", result, buttons.actions)
	}

	controller.Observe(rx, settings)
	result = controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 4))
	if result.State != StateNavigating || result.Navigation.Paused || len(buttons.actions) != 2 {
		t.Fatalf("fresh protocol and LCD RX did not resume: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestOperateHomeMatcherRejectsNearMatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  int
		text string
	}{
		{name: "wrong power scale", row: 0, text: "       0   100  200  300  400"},
		{name: "wrong current scale", row: 2, text: "       0  10.0  20  30.0  40"},
		{name: "overlay row", row: 4, text: "WARNING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := operateHomeScreen()
			state.SetRow(tc.row, tc.text)
			if matchesHome(state) {
				t.Fatalf("near-match OPERATE screen was accepted: row=%d text=%q", tc.row, tc.text)
			}
		})
	}
}

func TestControllerNeverTogglesOrSavesDuringTX(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prepare    func(*Controller, *recordingButtons, Settings)
		screen     display.State
		wantAction string
	}{
		{
			name: "fan value toggle",
			prepare: func(controller *Controller, buttons *recordingButtons, settings Settings) {
				driveToFanManagement(controller, settings, PolicyNormal)
			},
			screen:     submenuScreen("FAN MANAGEMENT", "NORMAL"),
			wantAction: "set",
		},
		{
			name: "save",
			prepare: func(controller *Controller, buttons *recordingButtons, settings Settings) {
				driveToSave(controller, settings, PolicyHigh)
			},
			screen:     submenuScreen("SAVE", "CONTEST"),
			wantAction: "set",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buttons := &recordingButtons{}
			controller := NewController(buttons)
			settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
			tc.prepare(controller, buttons, settings)
			before := len(buttons.actions)
			controller.Observe(statusAt(81, "standby", true), settings)
			result := controller.ObserveDisplay(txObservation(tc.screen, 100))
			if result.State != StatePaused || len(buttons.actions) != before {
				t.Fatalf("TX sent %s: result=%+v actions=%v", tc.wantAction, result, buttons.actions)
			}
		})
	}
}

func TestControllerRechecksStatusFreshnessBeforeEveryNavigationWrite(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))

	controller.mu.Lock()
	controller.statusAt = time.Now().Add(-statusContactWindow - time.Second)
	controller.mu.Unlock()

	result := controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 2))
	if result.State != StateFailed || len(buttons.actions) != 1 {
		t.Fatalf("stale status did not stop navigation: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestControllerLatchesTransportFailureWithoutRetry(t *testing.T) {
	buttons := &recordingButtons{err: errors.New("serial unavailable")}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)

	result := controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if result.State != StateFailed || len(buttons.actions) != 1 {
		t.Fatalf("transport failure result=%+v actions=%v", result, buttons.actions)
	}
	if len(result.Navigation.ActionsTaken) != 0 || !result.Navigation.MayBeInMenu ||
		result.Navigation.RecoveryState != "operator-required" || result.Navigation.RecoveryInstructions == "" {
		t.Fatalf("transport failure receipt overclaimed or omitted recovery: %+v", result.Navigation)
	}
	controller.ObserveDisplay(rxObservation(homeScreen(), 2))
	if len(buttons.actions) != 1 {
		t.Fatalf("transport failure retried: %v", buttons.actions)
	}
	if current := controller.Current(); !current.Navigation.MayBeInMenu {
		t.Fatalf("an uncorrelated home frame cleared menu uncertainty: %+v", current.Navigation)
	}
}

func TestControllerRecoveryRequiresNewVerifiedStandbyHomeAndClearsOverride(t *testing.T) {
	buttons := &recordingButtons{err: errors.New("serial unavailable")}
	controller := NewController(buttons)
	if err := controller.SetManualOverride(PolicyHigh, 0); err != nil {
		t.Fatalf("SetManualOverride: %v", err)
	}
	disabled := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	standbyRX := statusAt(81, "standby", false)
	controller.Observe(standbyRX, disabled)
	failed := controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if failed.State != StateFailed || !failed.ManualOverride.Active {
		t.Fatalf("failed override did not latch: %+v", failed)
	}

	if err := controller.Recover(standbyRX, Settings{Enabled: true}); err == nil || !strings.Contains(err.Error(), "disable automatic") {
		t.Fatalf("enabled recovery error = %v", err)
	}
	if err := controller.Recover(standbyRX, disabled); err == nil || !strings.Contains(err.Error(), "newer checksum-valid home") {
		t.Fatalf("stale-display recovery error = %v", err)
	}

	controller.ObserveDisplay(rxObservation(homeScreen(), 2))
	if err := controller.Recover(statusAt(81, "operate", false), disabled); err == nil || !strings.Contains(err.Error(), "STANDBY") {
		t.Fatalf("operate recovery error = %v", err)
	}
	if err := controller.Recover(standbyRX, disabled); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	result := controller.Current()
	if result.State != StateDisabled || result.ManualOverride.Active || result.Navigation.State != "idle" || result.Navigation.MayBeInMenu {
		t.Fatalf("recovery did not clear failed override safely: %+v", result)
	}
}

func TestControllerRecoveryFailsClosedWhenPersistenceFails(t *testing.T) {
	buttons := &leaseRecordingButtons{}
	controller := NewController(buttons)
	failPersist := false
	controller.ConfigurePersistence(PersistentState{}, false, func(PersistentState) error {
		if failPersist {
			return errors.New("disk unavailable")
		}
		return nil
	})
	disabled := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	standbyRX := statusAt(81, "standby", false)
	rx := false
	controller.manualOverride = PolicyHigh
	controller.nav = navState{failed: true, leaseHeld: true, failureAfterGen: 1}
	controller.mayBeInMenu = true
	controller.lastDisplayGen = 2
	controller.lastDisplayKey = "home"
	controller.displayTX = &rx
	controller.displayOperate = &rx

	failPersist = true
	if err := controller.Recover(standbyRX, disabled); err == nil || !strings.Contains(err.Error(), "persist recovered") {
		t.Fatalf("Recover error = %v", err)
	}
	result := controller.Current()
	if result.State != StateFailed || !result.ManualOverride.Active || result.Navigation.State != "failed" {
		t.Fatalf("persistence failure cleared failed state in memory: %+v", result)
	}
	if buttons.releases != 0 || !controller.nav.leaseHeld {
		t.Fatalf("persistence failure released lease: releases=%d nav=%+v", buttons.releases, controller.nav)
	}

	failPersist = false
	if err := controller.Recover(standbyRX, disabled); err != nil {
		t.Fatal(err)
	}
	if buttons.releases != 1 || controller.nav.leaseHeld {
		t.Fatalf("successful recovery did not release lease exactly once: releases=%d nav=%+v", buttons.releases, controller.nav)
	}
}

func TestControllerDoesNotReportCurrentPolicyUntilVerifiedReturnHome(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)

	generation := uint64(1)
	observe := func(state display.State) Result {
		result := controller.ObserveDisplay(rxObservation(state, generation))
		generation++
		return result
	}
	observe(homeScreen())
	for _, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP    On", "START   Stby", "TEMP/FANS"} {
		observe(setupScreen(selected))
	}
	observe(submenuScreen("TEMPERATURE SCALE", "NORMAL"))
	observe(submenuScreen("FAN MANAGEMENT", "NORMAL"))
	observe(submenuScreen("FAN MANAGEMENT", "CONTEST"))
	observe(submenuScreen("SAVE", "CONTEST"))
	result := observe(storingScreen())
	if result.CurrentPolicy != PolicyUnknown || result.CurrentPolicyVerifiedAt != "" {
		t.Fatalf("policy was reported before return-home verification: %+v", result)
	}
	result = observe(homeScreen())
	if result.CurrentPolicy != PolicyHigh || result.CurrentPolicyVerifiedAt == "" {
		t.Fatalf("verified policy receipt missing after return home: %+v", result)
	}
}

func TestControllerDelaysNormalRestoreButNeverHighCooling(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 7, 30, 5, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.normalRestoreAfter = now.Add(normalRestoreCooldown)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}

	controller.current = PolicyHigh
	controller.currentVerifiedAt = now
	controller.lastVerifiedHighAt = now
	result := controller.Observe(statusAt(74, "standby", false), settings)
	if result.State != StateBlocked || !containsBlock(result.BlockedBy, "cooldown") || result.CooldownUntil == "" {
		t.Fatalf("Normal restore was not cooled down: %+v", result)
	}
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if len(buttons.actions) != 0 {
		t.Fatalf("cooldown allowed a menu write: %v", buttons.actions)
	}

	controller.current = PolicyNormal
	controller.lastVerifiedHighAt = now
	result = controller.Observe(statusAt(81, "standby", false), settings)
	if containsBlock(result.BlockedBy, "cooldown") || result.State != StatePending {
		t.Fatalf("high cooling was incorrectly delayed: %+v", result)
	}
}

func TestControllerConservativelyDelaysNormalAfterRestartWithUnknownPolicy(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.normalRestoreAfter = now.Add(normalRestoreCooldown)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}

	result := controller.Observe(statusAt(74, "standby", false), settings)
	if result.State != StateBlocked || !containsBlock(result.BlockedBy, "cooldown") {
		t.Fatalf("unknown post-restart policy was not conservatively cooled down: %+v", result)
	}
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	if len(buttons.actions) != 0 {
		t.Fatalf("post-restart cooldown allowed Normal write: %v", buttons.actions)
	}

	result = controller.Observe(statusAt(81, "standby", false), settings)
	if result.State != StatePending || containsBlock(result.BlockedBy, "cooldown") {
		t.Fatalf("post-restart cooldown delayed high cooling: %+v", result)
	}
}

func TestControllerPreservesHighSaveCooldownAcrossDisableAndReenable(t *testing.T) {
	controller := NewController(&recordingButtons{})
	now := time.Date(2026, 7, 30, 6, 30, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.normalRestoreAfter = time.Time{}
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.current = PolicyHigh
	controller.currentVerifiedAt = now
	controller.lastVerifiedHighAt = now

	controller.Observe(statusAt(74, "standby", false), Settings{
		DisplayProfile:     SupportedDisplayProfile,
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	})
	result := controller.Observe(statusAt(74, "standby", false), settings)
	if result.State != StateBlocked || !containsBlock(result.BlockedBy, "cooldown") {
		t.Fatalf("disable/re-enable bypassed the verified high-save cooldown: %+v", result)
	}
}

func TestControllerViewAppliesCooldownToFreshlyEvaluatedNormalPolicy(t *testing.T) {
	controller := NewController(&recordingButtons{})
	now := time.Date(2026, 7, 30, 6, 45, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.normalRestoreAfter = time.Time{}
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.desired = PolicyHigh
	controller.current = PolicyHigh
	controller.currentVerifiedAt = now
	controller.lastVerifiedHighAt = now

	result := controller.View(statusAt(74, "standby", false), settings)
	if result.DesiredPolicy != PolicyNormal || result.State != StateBlocked || !containsBlock(result.BlockedBy, "cooldown") {
		t.Fatalf("view omitted cooldown for freshly evaluated Normal policy: %+v", result)
	}
}

func TestControllerContactLossDoesNotClearFailedPolicyLatch(t *testing.T) {
	buttons := &recordingButtons{err: errors.New("serial unavailable")}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))

	stale := statusAt(81, "standby", false)
	stale.RecentContact = false
	controller.Observe(stale, settings)
	controller.Observe(statusAt(81, "standby", false), settings)
	result := controller.ObserveDisplay(rxObservation(homeScreen(), 2))
	if result.State != StateFailed || len(buttons.actions) != 1 {
		t.Fatalf("contact loss cleared failure latch: result=%+v actions=%v", result, buttons.actions)
	}

	controller.Observe(statusAt(74, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 3))
	if len(buttons.actions) != 1 {
		t.Fatalf("opposite-threshold request cleared failure latch: %v", buttons.actions)
	}

	disabled := Settings{
		HighTemperatureC:   80,
		NormalTemperatureC: 75,
	}
	controller.Observe(statusAt(74, "standby", false), disabled)
	buttons.err = nil
	controller.ObserveDisplay(rxObservation(homeScreen(), 4))
	if err := controller.Recover(statusAt(74, "standby", false), disabled); err != nil {
		t.Fatalf("verified recovery: %v", err)
	}
	controller.normalRestoreAfter = time.Time{}
	controller.Observe(statusAt(74, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 5))
	if len(buttons.actions) != 2 {
		t.Fatalf("verified recovery did not clear failure latch: %v", buttons.actions)
	}
}

func TestControllerCompletedReceiptDoesNotMaskLaterSafetyBlock(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 80, NormalTemperatureC: 75}
	controller.Observe(statusAt(81, "standby", false), settings)

	generation := uint64(1)
	observe := func(state display.State) {
		controller.ObserveDisplay(rxObservation(state, generation))
		generation++
	}
	observe(homeScreen())
	for _, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP    On", "START   Stby", "TEMP/FANS"} {
		observe(setupScreen(selected))
	}
	observe(submenuScreen("TEMPERATURE SCALE", "CONTEST"))
	observe(submenuScreen("FAN MANAGEMENT", "CONTEST"))
	observe(submenuScreen("SAVE", "CONTEST"))
	observe(storingScreen())
	observe(homeScreen())

	result := controller.Observe(statusAt(81, "standby", true), settings)
	if result.State != StateBlocked || !containsBlock(result.BlockedBy, "rx") || result.Navigation.State != "complete" {
		t.Fatalf("completed receipt masked later TX block: %+v", result)
	}
}

func TestManualContestOverrideNavigatesWhileAutomaticPolicyIsDisabled(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	var persisted PersistentState
	controller.ConfigurePersistence(PersistentState{}, false, func(state PersistentState) error {
		persisted = state
		return nil
	})
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	controller.Observe(statusAt(30, "standby", false), settings)
	if err := controller.SetManualOverride(PolicyHigh, 0); err != nil {
		t.Fatalf("SetManualOverride: %v", err)
	}
	controller.Observe(statusAt(30, "standby", false), settings)

	driveToFanManagement(controller, settings, PolicyNormal)
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", "NORMAL"), 20))
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", "CONTEST"), 21))
	controller.ObserveDisplay(rxObservation(submenuScreen("SAVE", "CONTEST"), 22))
	controller.ObserveDisplay(rxObservation(storingScreen(), 23))
	result := controller.ObserveDisplay(rxObservation(homeScreen(), 24))

	if result.CurrentPolicy != PolicyHigh || result.CurrentPolicyConfidence != "verified-live" ||
		!result.ManualOverride.Active || result.ManualOverride.Policy != PolicyHigh {
		t.Fatalf("unexpected manual override result: %+v", result)
	}
	if persisted.ManualOverride != PolicyHigh || persisted.LastVerifiedPolicy != PolicyHigh ||
		persisted.LastVerifiedSource != "manual-override" {
		t.Fatalf("unexpected persisted state: %+v", persisted)
	}
}

func TestManualOverrideSupersedesQueuedVerification(t *testing.T) {
	controller := NewController(&recordingButtons{})
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	if got := controller.Current(); !got.Verification.Requested {
		t.Fatalf("startup verification was not queued: %+v", got)
	}
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	controller.Observe(statusAt(30, "standby", false), settings)
	if err := controller.SetManualOverride(PolicyHigh, 0); err != nil {
		t.Fatalf("SetManualOverride: %v", err)
	}
	got := controller.Current()
	if got.Verification.Requested || got.DesiredPolicy != PolicyHigh || got.DesiredPolicySource != "manual-override" {
		t.Fatalf("manual override did not supersede queued verification: %+v", got)
	}
}

func TestManualNormalOverrideBypassesAutomaticRestoreCooldown(t *testing.T) {
	controller := NewController(&recordingButtons{})
	now := time.Date(2026, 7, 30, 17, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.current = PolicyHigh
	controller.currentConfidence = "verified-live"
	controller.currentVerifiedAt = now
	controller.lastVerifiedHighAt = now
	settings := Settings{Enabled: true, DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	controller.Observe(statusAt(30, "standby", false), settings)
	if got := controller.Current(); !containsBlock(got.BlockedBy, "cooldown") {
		t.Fatalf("automatic Normal restore was not held by cooldown: %+v", got)
	}
	if err := controller.SetManualOverride(PolicyNormal, 0); err != nil {
		t.Fatalf("SetManualOverride: %v", err)
	}
	got := controller.Observe(statusAt(30, "standby", false), settings)
	if containsBlock(got.BlockedBy, "cooldown") || got.DesiredPolicy != PolicyNormal {
		t.Fatalf("explicit Normal override remained in cooldown: %+v", got)
	}
}

func TestClearCompletedNormalOverrideIsServerOnlyAndFailClosed(t *testing.T) {
	now := time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	rx := false
	status := api.Status{Telemetry: api.Telemetry{OperatingState: "standby", TX: &rx}, RecentContact: true}
	settings := Settings{Enabled: false}
	buttons := &recordingButtons{sent: true}
	var persisted PersistentState
	controller := NewController(buttons)
	controller.now = func() time.Time { return now }
	controller.persist = func(state PersistentState) error {
		persisted = state
		return nil
	}
	controller.manualOverride = PolicyNormal
	controller.current = PolicyNormal
	controller.currentConfidence = "verified-live"
	controller.currentVerifiedAt = now.Add(-time.Minute)
	controller.currentSource = "manual-override"
	controller.nav.step = "complete"
	controller.lastVerifiedScreen = "home:standby"
	controller.lastDisplayKey = "home"
	controller.lastDisplayGen = 1
	controller.displayTX = &rx
	controller.displayOperate = &rx
	controller.baseSettings = settings
	controller.settings = controller.effectiveSettingsLocked(settings)

	cleared, err := controller.ClearCompletedNormalOverride(status, settings)
	if err != nil || !cleared {
		t.Fatalf("ClearCompletedNormalOverride() = %v, %v", cleared, err)
	}
	if len(buttons.actions) != 0 {
		t.Fatalf("cleanup sent amplifier commands: %v", buttons.actions)
	}
	if persisted.ManualOverride != "" {
		t.Fatalf("persisted override was not cleared: %+v", persisted)
	}
	result := controller.Current()
	if result.ControlActive || result.ManualOverride.Active || result.Navigation.State != "idle" {
		t.Fatalf("cleanup left fan control active: %+v", result)
	}

	refused := []struct {
		name         string
		mutate       func(*Controller, *Settings)
		mutateStatus func(*api.Status)
	}{
		{name: "contest override", mutate: func(c *Controller, _ *Settings) { c.manualOverride = PolicyHigh }},
		{name: "automatic policy", mutate: func(_ *Controller, s *Settings) { s.Enabled = true }},
		{name: "unverified current policy", mutate: func(c *Controller, _ *Settings) { c.currentConfidence = "persisted-stale" }},
		{name: "unknown current policy", mutate: func(c *Controller, _ *Settings) { c.current = PolicyUnknown }},
		{name: "active navigation", mutate: func(c *Controller, _ *Settings) { c.nav.active = true }},
		{name: "paused navigation", mutate: func(c *Controller, _ *Settings) { c.nav.paused = true }},
		{name: "failed navigation", mutate: func(c *Controller, _ *Settings) { c.nav.failed = true }},
		{name: "held lease", mutate: func(c *Controller, _ *Settings) { c.nav.leaseHeld = true }},
		{name: "pending verification", mutate: func(c *Controller, _ *Settings) { c.verifyRequested = true }},
		{name: "missing verification time", mutate: func(c *Controller, _ *Settings) { c.currentVerifiedAt = time.Time{} }},
		{name: "amplifier may be in menu", mutate: func(c *Controller, _ *Settings) { c.mayBeInMenu = true }},
		{name: "missing home display", mutate: func(c *Controller, _ *Settings) { c.lastDisplayKey = "setup" }},
		{name: "missing display generation", mutate: func(c *Controller, _ *Settings) { c.lastDisplayGen = 0 }},
		{name: "unknown display RX", mutate: func(c *Controller, _ *Settings) { c.displayTX = nil }},
		{name: "display TX", mutate: func(c *Controller, _ *Settings) { value := true; c.displayTX = &value }},
		{name: "display OPERATE", mutate: func(c *Controller, _ *Settings) { value := true; c.displayOperate = &value }},
		{name: "stale protocol contact", mutateStatus: func(s *api.Status) { s.RecentContact = false }},
		{name: "protocol TX", mutateStatus: func(s *api.Status) { value := true; s.TX = &value }},
		{name: "unknown protocol RX", mutateStatus: func(s *api.Status) { s.TX = nil }},
		{name: "protocol OPERATE", mutateStatus: func(s *api.Status) { s.OperatingState = "operate" }},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			candidateSettings := settings
			candidateStatus := status
			candidateButtons := &recordingButtons{sent: true}
			candidate := NewController(candidateButtons)
			candidate.manualOverride = PolicyNormal
			candidate.current = PolicyNormal
			candidate.currentConfidence = "verified-live"
			candidate.currentVerifiedAt = now.Add(-time.Minute)
			candidate.lastDisplayKey = "home"
			candidate.lastDisplayGen = 1
			candidate.displayTX = &rx
			candidate.displayOperate = &rx
			if tc.mutate != nil {
				tc.mutate(candidate, &candidateSettings)
			}
			if tc.mutateStatus != nil {
				tc.mutateStatus(&candidateStatus)
			}
			expectedOverride := candidate.manualOverride
			cleared, err := candidate.ClearCompletedNormalOverride(candidateStatus, candidateSettings)
			if err != nil || cleared || candidate.manualOverride != expectedOverride {
				t.Fatalf("unsafe cleanup result: cleared=%v err=%v override=%q", cleared, err, candidate.manualOverride)
			}
			if len(candidateButtons.actions) != 0 {
				t.Fatalf("refused cleanup sent amplifier commands: %v", candidateButtons.actions)
			}
		})
	}
}

func TestClearCompletedNormalOverrideRollsBackOnPersistenceFailure(t *testing.T) {
	now := time.Date(2026, 7, 31, 18, 0, 0, 0, time.UTC)
	rx := false
	status := api.Status{Telemetry: api.Telemetry{OperatingState: "standby", TX: &rx}, RecentContact: true}
	settings := Settings{Enabled: false}
	controller := NewController()
	controller.manualOverride = PolicyNormal
	controller.current = PolicyNormal
	controller.currentConfidence = "verified-live"
	controller.currentVerifiedAt = now.Add(-time.Minute)
	controller.lastDisplayKey = "home"
	controller.lastDisplayGen = 1
	controller.displayTX = &rx
	controller.displayOperate = &rx
	controller.baseSettings = settings
	controller.settings = controller.effectiveSettingsLocked(settings)
	controller.persist = func(PersistentState) error { return errors.New("disk unavailable") }

	cleared, err := controller.ClearCompletedNormalOverride(status, settings)
	if err == nil || cleared {
		t.Fatalf("expected persistence failure, got cleared=%v err=%v", cleared, err)
	}
	if !controller.Current().ManualOverride.Active || controller.manualOverride != PolicyNormal {
		t.Fatalf("persistence failure did not restore override: %+v", controller.Current())
	}
}

func TestTimedManualOverrideExpiresBackToAutomatic(t *testing.T) {
	controller := NewController()
	now := time.Date(2026, 7, 30, 17, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	var persisted PersistentState
	controller.ConfigurePersistence(PersistentState{}, false, func(state PersistentState) error {
		persisted = state
		return nil
	})
	if err := controller.SetManualOverride(PolicyHigh, 15*time.Minute); err != nil {
		t.Fatalf("SetManualOverride: %v", err)
	}
	if got := controller.Current(); !got.ManualOverride.Active || got.ManualOverride.Until != "" ||
		got.ManualOverride.DurationMinutes != 15 {
		t.Fatalf("timed override did not wait for verification before starting: %+v", got)
	}
	if persisted.ManualOverrideDurationMinutes != 15 || persisted.ManualOverrideUntil != "" {
		t.Fatalf("pending duration was not persisted safely: %+v", persisted)
	}
	controller.mu.Lock()
	controller.recordVerifiedPolicyLocked(PolicyHigh, now, "manual-override")
	controller.mu.Unlock()
	if got := controller.Tick(now); got.ManualOverride.Until == "" || got.ManualOverride.DurationMinutes != 0 {
		t.Fatalf("verified override did not start its expiry countdown: %+v", got)
	}
	result := controller.Tick(now.Add(16 * time.Minute))
	if result.ManualOverride.Active || persisted.ManualOverride != "" ||
		persisted.ManualOverrideDurationMinutes != 0 || persisted.ManualOverrideUntil != "" {
		t.Fatalf("timed override did not expire: result=%+v persisted=%+v", result, persisted)
	}
}

func TestStartupVerificationRefreshesPersistedStaleReceipt(t *testing.T) {
	controller := NewController(&recordingButtons{})
	controller.ConfigurePersistence(PersistentState{
		LastVerifiedPolicy: PolicyHigh,
		LastVerifiedAt:     "2026-07-30T16:00:00Z",
		LastVerifiedSource: "automatic-temperature",
	}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	if got := controller.Current(); got.CurrentPolicy != PolicyHigh || got.CurrentPolicyConfidence != "persisted-stale" {
		t.Fatalf("persisted receipt was not exposed as stale: %+v", got)
	}
	controller.Observe(statusAt(30, "standby", false), settings)
	driveToFanManagement(controller, settings, PolicyHigh)
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", "CONTEST"), 20))
	controller.ObserveDisplay(rxObservation(submenuScreen("SAVE", "CONTEST"), 21))
	controller.ObserveDisplay(rxObservation(storingScreen(), 22))
	result := controller.ObserveDisplay(rxObservation(homeScreen(), 23))
	if result.CurrentPolicy != PolicyHigh || result.CurrentPolicyConfidence != "verified-live" ||
		result.CurrentPolicySource != "startup-verification" || result.Verification.Requested {
		t.Fatalf("startup verification did not refresh receipt: %+v", result)
	}
}

func TestStartupVerificationTimeoutUsesVerifiedFirstSeriesDisplayExit(t *testing.T) {
	buttons := &leaseRecordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)

	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	for generation, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP"} {
		controller.ObserveDisplay(rxObservation(setupScreen(selected), uint64(generation+2)))
	}
	if got := buttons.actions[len(buttons.actions)-1]; got != "right" {
		t.Fatalf("startup verification did not wait for START after BEEP: %v", buttons.actions)
	}

	now = now.Add(navigationTimeout + time.Millisecond)
	result := controller.Tick(now)
	if result.State != StateNavigating || result.Navigation.State != "recover:home" ||
		result.Navigation.LastAction != "display" || strings.Count(strings.Join(buttons.actions, ","), "display") != 1 {
		t.Fatalf("timeout did not start one bounded DISPLAY exit: result=%+v actions=%v", result, buttons.actions)
	}
	if !result.Navigation.MayBeInMenu || buttons.releases != 0 {
		t.Fatalf("recovery released safety ownership before verified home: result=%+v releases=%d", result, buttons.releases)
	}

	result = controller.ObserveDisplay(rxObservation(homeScreen(), 7))
	if result.State != StateFailed || result.Navigation.State != "failed" ||
		result.Navigation.RecoveryState != "verified-home" || result.Navigation.MayBeInMenu ||
		result.Navigation.LastVerifiedScreen != "home:standby" || buttons.releases != 1 {
		t.Fatalf("verified DISPLAY exit did not fail closed at home: result=%+v releases=%d", result, buttons.releases)
	}
	if !result.Verification.Requested || !strings.Contains(result.Navigation.LastError, "timed out") {
		t.Fatalf("verified recovery lost the failed verification receipt: %+v", result)
	}
}

func TestStartupVerificationTimeoutUsesVerifiedSecondSeriesDisplayExit(t *testing.T) {
	buttons := &leaseRecordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.ModelName = "EXPERT 1.5K-FA"
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	home := homeScreen()
	home.SetRow(1, "                       EXPERT 1.5K-FA")
	controller.ObserveDisplay(rxObservation(home, 1))
	for generation, selected := range []string{"CONFIG", "ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP"} {
		controller.ObserveDisplay(rxObservation(secondSeriesSetupScreen(selected), uint64(generation+2)))
	}

	now = now.Add(navigationTimeout + time.Millisecond)
	result := controller.Tick(now)
	if result.State != StateNavigating || result.Navigation.State != "recover:home" ||
		result.Navigation.LastAction != "display" || strings.Count(strings.Join(buttons.actions, ","), "display") != 1 {
		t.Fatalf("Second Series timeout did not start one bounded DISPLAY exit: result=%+v actions=%v", result, buttons.actions)
	}
	if !result.Navigation.MayBeInMenu || buttons.releases != 0 {
		t.Fatalf("Second Series recovery released safety ownership before verified home: result=%+v releases=%d", result, buttons.releases)
	}

	result = controller.ObserveDisplay(rxObservation(home, 8))
	if result.State != StateFailed || result.Navigation.State != "failed" ||
		result.Navigation.RecoveryState != "verified-home" || result.Navigation.MayBeInMenu ||
		result.Navigation.LastVerifiedScreen != "home:standby" || buttons.releases != 1 {
		t.Fatalf("Second Series DISPLAY exit did not fail closed at home: result=%+v releases=%d", result, buttons.releases)
	}
	if !result.Verification.Requested || !strings.Contains(result.Navigation.LastError, "timed out") {
		t.Fatalf("Second Series recovery lost the failed verification receipt: %+v", result)
	}
}

func TestSecondSeriesStartupVerificationDisplayExitRejectsNewerNonHomeFrame(t *testing.T) {
	buttons := &leaseRecordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.ModelName = "EXPERT 1.5K-FA"
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	home := homeScreen()
	home.SetRow(1, "                       EXPERT 1.5K-FA")
	controller.ObserveDisplay(rxObservation(home, 1))
	for generation, selected := range []string{"CONFIG", "ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP"} {
		controller.ObserveDisplay(rxObservation(secondSeriesSetupScreen(selected), uint64(generation+2)))
	}

	now = now.Add(navigationTimeout + time.Millisecond)
	controller.Tick(now)
	result := controller.ObserveDisplay(rxObservation(secondSeriesSetupScreen("BEEP"), 8))
	if result.State != StateFailed || result.Navigation.RecoveryState != "operator-required" ||
		!result.Navigation.MayBeInMenu || buttons.releases != 1 ||
		strings.Count(strings.Join(buttons.actions, ","), "display") != 1 {
		t.Fatalf("newer non-home recovery frame did not fail closed: result=%+v actions=%v releases=%d", result, buttons.actions, buttons.releases)
	}
	if !strings.Contains(result.Navigation.LastError, "did not return to a checksum-valid STANDBY/RX home display") {
		t.Fatalf("newer non-home recovery frame lost its diagnostic: %+v", result)
	}
}

func TestStartupVerificationDisplayExitRejectsCrossFamilySetupScreen(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 2))

	result := controller.ObserveDisplay(rxObservation(secondSeriesSetupScreen("CAT"), 3))
	if result.State != StateFailed || result.Navigation.RecoveryState != "operator-required" ||
		strings.Contains(strings.Join(buttons.actions, ","), "display") {
		t.Fatalf("cross-family setup screen authorized DISPLAY recovery: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestSecondSeriesStartupVerificationDisplayExitRejectsFirstSeriesSetupScreen(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.ModelName = "EXPERT 1.5K-FA"
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	home := homeScreen()
	home.SetRow(1, "                       EXPERT 1.5K-FA")
	controller.ObserveDisplay(rxObservation(home, 1))
	controller.ObserveDisplay(rxObservation(secondSeriesSetupScreen("CONFIG"), 2))

	result := controller.ObserveDisplay(rxObservation(setupScreen("CAT"), 3))
	if result.State != StateFailed || result.Navigation.RecoveryState != "operator-required" ||
		strings.Contains(strings.Join(buttons.actions, ","), "display") {
		t.Fatalf("First Series setup screen authorized Second Series DISPLAY recovery: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestStartupVerificationDisplayExitIsAttemptedOnlyOnce(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 2))

	now = now.Add(navigationTimeout + time.Millisecond)
	controller.Tick(now)
	now = now.Add(navigationTimeout + time.Millisecond)
	result := controller.Tick(now)
	if result.State != StateFailed || strings.Count(strings.Join(buttons.actions, ","), "display") != 1 {
		t.Fatalf("DISPLAY recovery was retried: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestStartupVerificationDisplayExitWriteFailurePreservesDiagnostic(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 2))
	buttons.err = errors.New("serial unavailable")

	now = now.Add(navigationTimeout + time.Millisecond)
	result := controller.Tick(now)
	if result.State != StateFailed || !strings.Contains(result.Navigation.LastError, "send display: serial unavailable") ||
		strings.Count(strings.Join(buttons.actions, ","), "display") != 1 {
		t.Fatalf("DISPLAY write failure lost its diagnostic or retried: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestStartupVerificationDisplayExitFailsClosedWithoutLCDEvidenceOfRX(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	controller.ObserveDisplay(rxObservation(setupScreen("ANTENNA"), 2))

	controller.Observe(statusAt(30, "standby", true), settings)
	controller.ObserveDisplay(txObservation(setupScreen("ANTENNA"), 3))
	now = now.Add(time.Second)
	rxStatus := statusAt(30, "standby", false)
	rxStatus.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(rxStatus, settings)
	now = now.Add(navigationTimeout + time.Millisecond)
	result := controller.Tick(now)

	if result.State != StateFailed || result.Navigation.State != "failed" ||
		strings.Contains(strings.Join(buttons.actions, ","), "display") || result.Navigation.Paused {
		t.Fatalf("RX-blocked DISPLAY recovery did not fail closed: result=%+v actions=%v", result, buttons.actions)
	}
	if !strings.Contains(result.Navigation.LastError, "timed out waiting for the exact expected LCD waypoint after fresh RX") {
		t.Fatalf("RX-blocked DISPLAY recovery lost timeout diagnostic: %+v", result)
	}
}

func TestStartupVerificationDoesNotRecoverFromStaleSetupFrameAfterSubmenuSET(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	for generation, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP", "START", "TEMP/FANS"} {
		controller.ObserveDisplay(rxObservation(setupScreen(selected), uint64(generation+2)))
	}
	if got := buttons.actions[len(buttons.actions)-1]; got != "set" {
		t.Fatalf("startup verification did not attempt to enter TEMP/FANS: %v", buttons.actions)
	}

	now = now.Add(navigationTimeout + time.Millisecond)
	result := controller.Tick(now)
	if result.State != StateFailed || result.Navigation.RecoveryState != "operator-required" ||
		strings.Contains(strings.Join(buttons.actions, ","), "display") {
		t.Fatalf("stale pre-SET setup frame authorized DISPLAY recovery: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestStartupVerificationDoesNotRecoverFromSetupFrameWhileAwaitingSubmenu(t *testing.T) {
	buttons := &recordingButtons{}
	controller := NewController(buttons)
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return now }
	controller.ConfigurePersistence(PersistentState{}, true, nil)
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	status := statusAt(30, "standby", false)
	status.LastContactAt = now.Format(time.RFC3339Nano)
	controller.Observe(status, settings)
	controller.ObserveDisplay(rxObservation(homeScreen(), 1))
	for generation, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP", "START", "TEMP/FANS"} {
		controller.ObserveDisplay(rxObservation(setupScreen(selected), uint64(generation+2)))
	}

	result := controller.ObserveDisplay(rxObservation(setupScreen("BEEP"), 9))
	if result.State != StateFailed || result.Navigation.RecoveryState != "operator-required" ||
		strings.Contains(strings.Join(buttons.actions, ","), "display") {
		t.Fatalf("setup frame while awaiting submenu authorized DISPLAY recovery: result=%+v actions=%v", result, buttons.actions)
	}
}

func TestPassiveFrontPanelSaveUpdatesVerifiedPolicyOnlyAfterStoringAndHome(t *testing.T) {
	controller := NewController()
	var persisted PersistentState
	controller.ConfigurePersistence(PersistentState{}, false, func(state PersistentState) error {
		persisted = state
		return nil
	})
	settings := Settings{DisplayProfile: SupportedDisplayProfile, HighTemperatureC: 50, NormalTemperatureC: 42}
	controller.Observe(statusAt(30, "standby", false), settings)
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", "CONTEST"), 1))
	controller.ObserveDisplay(rxObservation(submenuScreen("SAVE", "CONTEST"), 2))
	if got := controller.Current(); got.CurrentPolicy != PolicyUnknown {
		t.Fatalf("unsaved front-panel selection was treated as verified: %+v", got)
	}
	controller.ObserveDisplay(rxObservation(storingScreen(), 3))
	result := controller.ObserveDisplay(rxObservation(homeScreen(), 4))
	if result.CurrentPolicy != PolicyHigh || result.CurrentPolicyConfidence != "verified-live" ||
		result.CurrentPolicySource != "observed-front-panel-save" {
		t.Fatalf("passive saved mode was not verified: %+v", result)
	}
	if persisted.LastVerifiedPolicy != PolicyHigh || persisted.LastVerifiedSource != "observed-front-panel-save" {
		t.Fatalf("passive saved mode was not persisted: %+v", persisted)
	}
}

func screen(rows ...string) display.State {
	state := display.NewState()
	for row, text := range rows {
		state.SetRow(row, text)
	}
	return state
}

func rxObservation(state display.State, generation uint64) DisplayObservation {
	tx := false
	operate := matchesOperateHome(state)
	return DisplayObservation{State: state, Generation: generation, TX: &tx, Operate: &operate}
}

func txObservation(state display.State, generation uint64) DisplayObservation {
	tx := true
	operate := matchesOperateHome(state)
	return DisplayObservation{State: state, Generation: generation, TX: &tx, Operate: &operate}
}

func driveToFanManagement(controller *Controller, settings Settings, currentPolicy string) {
	controller.Observe(statusAt(81, "standby", false), settings)
	generation := uint64(1)
	observe := func(state display.State) {
		controller.ObserveDisplay(rxObservation(state, generation))
		generation++
	}
	observe(homeScreen())
	for _, selected := range []string{"ANTENNA", "CAT", "MANUAL TUNE", "DISPLAY", "BEEP", "START", "TEMP/FANS"} {
		observe(setupScreenWithValues(selected, "On", "Oper"))
	}
	observe(submenuScreen("TEMPERATURE SCALE", policyDisplayValue(currentPolicy)))
}

func driveToSave(controller *Controller, settings Settings, targetPolicy string) {
	currentPolicy := PolicyNormal
	if targetPolicy == PolicyNormal {
		currentPolicy = PolicyHigh
	}
	driveToFanManagement(controller, settings, currentPolicy)
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", policyDisplayValue(currentPolicy)), 20))
	controller.ObserveDisplay(rxObservation(submenuScreen("FAN MANAGEMENT", policyDisplayValue(targetPolicy)), 21))
}

func policyDisplayValue(policy string) string {
	if policy == PolicyHigh {
		return "CONTEST"
	}
	return strings.ToUpper(policy)
}

func highlight(state *display.State, row, start, end int) {
	for col := start; col < end; col++ {
		state.SetAttr(row, col, 1)
	}
}

func homeScreen() display.State {
	return homeScreenAt("26 C")
}

func homeScreenAt(temperature string) display.State {
	return screen(
		"",
		"                       EXPERT 1.3K-FA",
		"                       Solid State",
		"                       Fully Automatic",
		"                Standby",
		"",
		"IN  BAND ANT BNK  CAT   OUT   SWR   TEMP",
		" 2   40m  4b  A  KENWD  LOW  --.--  "+temperature,
	)
}

func operateHomeScreen() display.State {
	return screen(
		"       0   125  250  375  500",
		"PA OUT                           0 W pep",
		"       0  12.5  25  37.5  50",
		"I PA                           0.0 A",
		"",
		"",
		"IN  BAND ANT BNK  CAT   OUT   SWR   TEMP",
		" 2   20m  4b  A  KENWD  LOW  --.--  37 C",
	)
}

func setupScreen(selected string) display.State {
	return setupScreenWithValues(selected, "On", "Stby")
}

func setupScreenWithValues(selected, beep, start string) display.State {
	beepText := "BEEP    " + beep
	startText := "START   " + start
	state := screen(
		"       SETUP OPTIONS vs. INPUT 2",
		" ANTENNA       "+beepText+"     TUN ANT",
		" CAT           "+startText+"   RX  ANT",
		" MANUAL TUNE   TEMP/FANS      BANK",
		" DISPLAY       ALARMS LOG     EXIT",
		"",
		"",
		" [  ][  ]:SELECT          [SET]:CONFIRM",
	)
	positions := map[string][3]int{
		"ANTENNA":      {1, 0, 13},
		"CAT":          {2, 0, 13},
		"MANUAL TUNE":  {3, 0, 13},
		"DISPLAY":      {4, 0, 13},
		"BEEP":         {1, 14, 28},
		"START":        {2, 14, 28},
		"BEEP    On":   {1, 14, 28},
		"START   Stby": {2, 14, 28},
		"TEMP/FANS":    {3, 14, 28},
	}
	position := positions[selected]
	highlight(&state, position[0], position[1], position[2])
	return state
}

func secondSeriesSetupScreen(selected string) display.State {
	state := screen(
		"       SETUP OPTIONS vs. INPUT 1",
		" CONFIG       DISPLAY       ALARMS LOG",
		" ANTENNA      BEEP    Off   TUN ANT",
		" CAT          START   Stby  RX ANT",
		" MANUAL TUNE  TEMP/FANS     EXIT",
		"",
		"",
		" [  ][  ]:SELECT          [SET]:CONFIRM",
	)
	positions := map[string][3]int{
		"CONFIG":      {1, 0, 13},
		"ANTENNA":     {2, 0, 13},
		"CAT":         {3, 0, 13},
		"MANUAL TUNE": {4, 0, 13},
		"DISPLAY":     {1, 14, 28},
		"BEEP":        {2, 14, 28},
		"START":       {3, 14, 28},
		"TEMP/FANS":   {4, 14, 28},
	}
	position := positions[selected]
	highlight(&state, position[0], position[1], position[2])
	return state
}

func submenuScreen(selected, policy string) display.State {
	state := screen(
		"          TEMPERATURE AND FANS",
		"",
		"   TEMPERATURE SCALE   CELSIUS",
		"   FAN MANAGEMENT      "+policy,
		"                                  SAVE",
		"",
		"",
		" [  ][  ]:SELECT          [SET]:CONFIRM",
	)
	switch selected {
	case "TEMPERATURE SCALE":
		highlight(&state, 2, 2, 21)
	case "FAN MANAGEMENT":
		highlight(&state, 3, 2, 18)
	case "SAVE":
		highlight(&state, 4, 32, 39)
	}
	return state
}

func storingScreen() display.State {
	return screen(
		"          TEMPERATURE AND FANS",
		"",
		"             STORING DATA!",
		"",
		"",
		"",
		"         SAVE SETTINGS AND EXIT",
		" [  ][  ]:SELECT          [SET]:CONFIRM",
	)
}
