package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/api"
	"github.com/FtlC-ian/expert-amp-server/internal/apidocs"
	"github.com/FtlC-ian/expert-amp-server/internal/config"
	"github.com/FtlC-ian/expert-amp-server/internal/display"
	"github.com/FtlC-ian/expert-amp-server/internal/font"
	"github.com/FtlC-ian/expert-amp-server/internal/protocol"
	"github.com/FtlC-ian/expert-amp-server/internal/runtime"
	"github.com/FtlC-ian/expert-amp-server/internal/serial"
	"github.com/FtlC-ian/expert-amp-server/internal/server"
	"github.com/FtlC-ian/expert-amp-server/internal/transport"
)

//go:embed index.html
var webFS embed.FS

var (
	Version   = "dev"
	Commit    = ""
	BuildDate = ""
	Channel   = "dev"
)

func main() {
	addr := flag.String("addr", ":8088", "listen address")
	configPath := flag.String("config", "config/expert-amp-server.json", "path to local runtime config")
	pollInterval := flag.Duration("poll-interval", 200*time.Millisecond, "snapshot poll interval")
	lcdFlagDebug := flag.Bool("lcd-flag-debug", false, "log changes in unknown GetLCD flag bits for protocol investigation")
	flag.Parse()

	if err := run(*addr, *configPath, *pollInterval, *lcdFlagDebug); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func run(addr, configPath string, pollInterval time.Duration, lcdFlagDebug bool) error {
	cfg, err := config.NewManager(configPath, addr)
	if err != nil {
		return err
	}

	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	handler, snapshot, poller, serialSource, requestRestart := newServer(cfg, pollInterval, stop, lcdFlagDebug)

	srv := &http.Server{
		Addr:    snapshot.Settings.ListenAddress,
		Handler: handler,
	}

	ctx := requestRestart.attach(baseCtx)

	if serialSource != nil {
		serialSource.Start(ctx)
		log.Printf("live serial ingest started on %s", snapshot.Settings.SerialPort)
	}

	go func() {
		if err := poller.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("snapshot poller stopped: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("http shutdown failed: %v", err)
		}
	}()

	log.Printf("expert-amp-server %s starting (commit=%s buildDate=%s channel=%s)", Version, Commit, BuildDate, Channel)
	log.Printf("expert-amp-server server listening on %s (config: %s)", srv.Addr, snapshot.Path)
	err = srv.ListenAndServe()
	if requestRestart.requested() && (err == nil || errors.Is(err, http.ErrServerClosed)) {
		return nil
	}
	return err
}

type restartSignal struct {
	ch chan struct{}
}

func newRestartSignal() *restartSignal {
	return &restartSignal{ch: make(chan struct{}, 1)}
}

func (r *restartSignal) request(context.Context) error {
	go func() {
		// Let the HTTP handler return its JSON response before the process starts
		// graceful shutdown. Without this small deferral, clients can observe an
		// empty reply even though systemd restarts the service correctly.
		time.Sleep(250 * time.Millisecond)
		select {
		case r.ch <- struct{}{}:
		default:
		}
	}()
	return nil
}

func (r *restartSignal) requested() bool {
	select {
	case <-r.ch:
		select {
		case r.ch <- struct{}{}:
		default:
		}
		return true
	default:
		return false
	}
}

func (r *restartSignal) attach(parent context.Context) context.Context {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-parent.Done():
		case <-r.ch:
			cancel()
		}
	}()
	return ctx
}

func newServer(cfg *config.Manager, pollInterval time.Duration, stop context.CancelFunc, lcdFlagDebugOpt ...bool) (http.Handler, config.Snapshot, *runtime.Poller, *runtime.SerialSource, *restartSignal) {
	lcdFlagDebug := false
	if len(lcdFlagDebugOpt) > 0 {
		lcdFlagDebug = lcdFlagDebugOpt[0]
	}
	signal := newRestartSignal()
	snapshot := cfg.Get()
	rom := font.Builtin()
	demoState := display.DemoState()
	altState := display.DemoStateAlt()
	fixtures := loadFixtures(demoState)

	telemetry := protocol.TelemetryFromDisplayState(fixtures.States["home"], "fixture:home")
	telemetry.Confidence = "fixture-derived"
	telemetry.Notes = append([]string{"fixture default mirrors current captured home screen"}, telemetry.Notes...)

	store := runtime.NewStore(runtime.Snapshot{
		State:     fixtures.States["home"],
		Telemetry: telemetry,
		Frame:     fixtures.Frames["home"],
		FrameKind: "home",
		Source:    "fixture:home",
		Sequence:  1,
		UpdatedAt: time.Now().UTC(),
	})

	var source runtime.Source
	statusState := runtime.NewStatusState(api.Status{})
	var serialSource *runtime.SerialSource

	if snapshot.Settings.SerialPort != "" && snapshot.Settings.PollingMode != string(config.PollingModeOff) {
		defaults := runtime.DefaultSerialSourceConfig(snapshot.Settings.SerialPort)
		serialCfg := runtime.SerialSourceConfig{
			Port:                      snapshot.Settings.SerialPort,
			BaudRate:                  snapshot.Settings.SerialBaudRate,
			ReadTimeout:               time.Duration(snapshot.Settings.SerialReadTimeoutMs) * time.Millisecond,
			ReadSize:                  512,
			PollingMode:               snapshot.Settings.PollingMode,
			PollInterval:              time.Duration(snapshot.Settings.PollIntervalMs) * time.Millisecond,
			DisplayPollEnabled:        snapshot.Settings.DisplayPollingEnabled,
			DisplayPollInterval:       time.Duration(snapshot.Settings.PollIntervalMs) * time.Millisecond,
			DisplayPollFrameHex:       defaults.DisplayPollFrameHex,
			StatusPollCommandEnabled:  snapshot.Settings.StatusPollCommandEnabled,
			StatusPollCommandInterval: time.Duration(snapshot.Settings.PollIntervalMs) * time.Millisecond,
			StatusPollCommandFrameHex: defaults.StatusPollCommandFrameHex,
			AssertDTR:                 snapshot.Settings.SerialAssertDTR,
			AssertRTS:                 snapshot.Settings.SerialAssertRTS,
			LCDFlagDebug:              lcdFlagDebug,
			PollingModeFn: func() string {
				return cfg.Get().Settings.PollingMode
			},
			DisplayPollEnabledFn: func() bool {
				return cfg.Get().Settings.DisplayPollingEnabled
			},
			StatusPollEnabledFn: func() bool {
				settings := cfg.Get().Settings
				return settings.StatusPollingEnabled && settings.StatusPollCommandEnabled
			},
		}
		serialSource = runtime.NewSerialSource(serialCfg, serial.OpenRealPort{}, runtime.Update{
			State:     fixtures.States["home"],
			Telemetry: telemetry,
			Frame:     fixtures.Frames["home"],
			FrameKind: "home",
			Source:    "fixture:home",
		})
		source = serialSource
		log.Printf("serial source configured on %s, will start live ingest", snapshot.Settings.SerialPort)
	} else {
		source = runtime.FixtureSource{
			Catalog:   fixtures,
			Kind:      "home",
			Telemetry: telemetry,
		}
		if snapshot.Settings.SerialPort == "" {
			log.Printf("no serial port configured, using fixture source")
		} else {
			log.Printf("display polling disabled, using fixture source")
		}
	}

	poller := &runtime.Poller{
		Source:   source,
		Store:    store,
		Interval: pollInterval,
		Logger:   log.Default(),
	}

	indexHTML, err := webFS.ReadFile("index.html")
	if err != nil {
		log.Fatal(err)
	}

	var buttonTransport transport.ButtonTransport
	var wakeTransport transport.WakeTransport
	if serialSource != nil {
		buttonTransport = serialSource
		wakeTransport = serialSource
		statusState = serialSource.StatusState()
	} else if snapshot.Settings.SerialPort != "" {
		buttonTransport = transport.NewLocalButtonTransport(snapshot.Settings.SerialPort, serial.OpenRealPort{}, transport.DefaultButtonTimeout)
		wakeTransport = transport.NewLocalWakeTransport(snapshot.Settings.SerialPort, serial.OpenRealPort{}, transport.DefaultButtonTimeout)
	}

	handler := server.NewHandler(server.Options{
		IndexHTML:       indexHTML,
		DocsHTML:        apidocs.MustDocsHTML(),
		OpenAPIJSON:     apidocs.MustOpenAPIJSON(),
		ROM:             rom,
		Store:           store,
		StatusState:     statusState,
		SerialSource:    serialSource,
		DemoState:       demoState,
		AltState:        altState,
		Fixtures:        fixtures,
		Config:          cfg,
		ButtonTransport: buttonTransport,
		WakeTransport:   wakeTransport,
		Version:         server.VersionInfo{Version: Version, Commit: Commit, BuildDate: BuildDate, Channel: Channel},
		RestartServer:   signal.request,
	})

	return handler, snapshot, poller, serialSource, signal
}

func loadFixtures(fallback display.State) runtime.FixtureCatalog {
	states := map[string]display.State{}
	frames := map[string]api.FrameInfo{}

	fixtures := map[string]string{
		"home":  "fixtures/real_home_status_frame.bin",
		"menu":  "fixtures/real_menu_frame.bin",
		"panel": "fixtures/real_panel_frame.bin",
	}
	for key, path := range fixtures {
		if state, meta, err := protocol.LoadFixtureState(path); err == nil {
			states[key] = state
			frame := api.FrameInfo{
				Source:      meta.Source,
				Length:      meta.Length,
				StartOffset: meta.StartOffset,
				ScreenText:  meta.ScreenText,
			}
			if meta.LCDFlags != nil {
				frame.LCDFlags = &api.LCDFlags{
					RawInverted:     meta.LCDFlags.RawInverted,
					Decoded:         meta.LCDFlags.Decoded,
					ChecksumPresent: meta.LCDFlags.ChecksumPresent,
					ChecksumValid:   meta.LCDFlags.ChecksumValid,
				}
			}
			frames[key] = frame
		}
	}
	if _, ok := states["home"]; !ok {
		states["home"] = fallback
		frames["home"] = api.FrameInfo{Source: "demo"}
	}

	return runtime.FixtureCatalog{States: states, Frames: frames}
}
