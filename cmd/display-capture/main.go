// display-capture passively records unique amplifier LCD states as NDJSON.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FtlC-ian/expert-amp-server/internal/capture"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "display-capture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("display-capture", flag.ContinueOnError)
	baseURL := flags.String("base-url", "http://127.0.0.1:8088", "expert-amp-server base URL")
	output := flags.String("output", "display-captures.ndjson", "append-only NDJSON output path")
	interval := flags.Duration("interval", time.Second, "snapshot polling interval")
	requestTimeout := flags.Duration("request-timeout", 5*time.Second, "timeout for each read-only GET")
	maxUnique := flags.Int("max-unique", 0, "stop after writing this many new unique states (0 = unlimited)")
	once := flags.Bool("once", false, "poll once and exit, whether or not the state was already captured")
	label := flags.String("transition-label", "", "operator-supplied label for the transition preceding the next newly captured state")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *requestTimeout <= 0 {
		return errors.New("request timeout must be positive")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stats, err := capture.Run(ctx, capture.Options{
		Client: capture.Client{
			BaseURL:    *baseURL,
			HTTPClient: &http.Client{Timeout: *requestTimeout},
		},
		OutputPath:      *output,
		PollInterval:    *interval,
		MaxUnique:       *maxUnique,
		Once:            *once,
		TransitionLabel: *label,
		OnRecord: func(record capture.Record) {
			fmt.Fprintf(os.Stderr, "captured sequence=%d hash=%s label=%q\n", record.Sequence, record.StateHash, record.TransitionLabel)
		},
	})
	fmt.Fprintf(os.Stderr, "polls=%d written=%d duplicates=%d output=%s\n", stats.Polls, stats.Written, stats.Skipped, *output)
	return err
}
