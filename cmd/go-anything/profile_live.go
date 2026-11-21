package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/app"
	profadapter "github.com/go-anything/go-anything/internal/adapter/profile"
	"github.com/go-anything/go-anything/internal/adapter/store"
	"github.com/go-anything/go-anything/internal/domain"
	"github.com/go-anything/go-anything/internal/usecase"
)

// maxLiveSeconds bounds how long a live profile samples. A longer window holds the service's profiler
// for minutes, and a benchmark-sized report does not need it.
const maxLiveSeconds = 600

// profileGrace is how long the HTTP client waits beyond the sampling window for the profile to arrive.
const profileGrace = 30 * time.Second

// liveOptions are the flags of `go-anything profile -url`.
type liveOptions struct {
	URL      string
	Cases    string // app recording replayed against URL while the profile is taken
	Seconds  int
	Bin      string // optional binary for pprof symbols
	Keep     string // where to keep the profile; empty means a temp file
	Recorded map[string]bool
}

// cmdProfileLive takes a CPU profile from a running service while the recorded app cases replay against it,
// then ranks the functions the same way the benchmark mode does. No profile is printed unless the whole
// profile arrived and the traffic reached the service.
func cmdProfileLive(o liveOptions, stdout io.Writer) error {
	if err := profadapter.ValidateBase(o.URL); err != nil {
		return err
	}
	if o.Cases == "" {
		return errors.New("profile: -url needs -cases <app.json>; without traffic the profile shows an idle service")
	}
	if o.Seconds < 1 || o.Seconds > maxLiveSeconds {
		return fmt.Errorf("profile: -seconds must be between 1 and %d, got %d", maxLiveSeconds, o.Seconds)
	}
	if o.Bin != "" {
		if _, err := os.Stat(o.Bin); err != nil {
			return fmt.Errorf("profile: -bin: %w", err)
		}
	}
	rec, err := store.LoadApp(o.Cases)
	if err != nil {
		return err
	}
	for _, c := range rec.Cases {
		if c.Kind != "http" {
			return fmt.Errorf("profile: live mode replays HTTP cases only; case %q is %q", c.Name, c.Kind)
		}
	}

	dir, err := os.MkdirTemp("", "go-anything-live-*")
	if err != nil {
		return fmt.Errorf("profile: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	keep := o.Keep
	if keep == "" {
		keep = filepath.Join(dir, "live-cpu.pb.gz")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Timeout: time.Duration(o.Seconds)*time.Second + profileGrace}
	var body []byte
	var fetchErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		body, fetchErr = profadapter.FetchCPUProfile(ctx, client, o.URL, o.Seconds)
	}()

	runner := &transportCapture{inner: app.Runner{BaseURL: o.URL}, base: o.URL}
	var tally replayTally
	// Each pass replays every case. The first pass always runs, so the profile never describes an idle
	// service by accident; later passes stop as soon as the profile request has finished.
	for {
		for _, c := range rec.Cases {
			if tally.requests > 0 && finished(done) {
				break
			}
			v, err := usecase.AppReplay(domain.AppRecording{Subject: rec.Subject, Cases: []domain.AppCase{c}}, runner)
			if err != nil {
				cancel()
				<-done
				return fmt.Errorf("profile: replay: %w", err)
			}
			if runner.err != nil {
				// The traffic did not reach the service, so the profile would not show the workload.
				cancel()
				<-done
				return runner.err
			}
			tally.add(v)
		}
		if tally.requests > 0 && finished(done) {
			break
		}
	}
	<-done
	if fetchErr != nil {
		return fetchErr
	}
	if err := profadapter.WriteProfile(keep, body); err != nil {
		return err
	}
	top, err := profadapter.Top(keep, o.Bin)
	if err != nil {
		return err
	}
	hot, err := domain.ParseTop(bytes.NewReader(top))
	if err != nil {
		return err
	}
	if !profadapter.HasSamples(top) {
		return fmt.Errorf("profile: no CPU samples in the %ds window while %d requests were replayed; the service did no CPU work, so there is nothing to rank. Raise -seconds or send heavier traffic", o.Seconds, tally.requests)
	}
	fmt.Fprintf(stdout, "profiled %s for %ds while replaying %d requests (%d case(s) answered differently from the recording)\n", o.URL, o.Seconds, tally.requests, tally.differed)
	for _, s := range tally.samples {
		fmt.Fprintf(stdout, "  %s\n", s)
	}
	printHot(stdout, "hot functions in live service "+o.URL, hot, o.Recorded)
	return nil
}

// replayTally counts the replayed requests. Differing answers are counted, not fatal: the profile is still
// of the service under this traffic. Only a request that never got an answer stops the run.
type replayTally struct {
	requests int
	differed int
	samples  []string // first few differences, for the operator
}

const maxTallySamples = 3

func (t *replayTally) add(v usecase.AppVerdict) {
	t.requests += v.Total
	if len(v.Mismatches) == 0 {
		return
	}
	t.differed++
	if len(t.samples) < maxTallySamples {
		m := v.Mismatches[0]
		t.samples = append(t.samples, fmt.Sprintf("%s: %s differs from the recording", m.Case, m.What))
	}
}

// transportCapture keeps the first transport failure of a replayed request. The usecase turns failures into
// mismatches, which lose the error type; this keeps it so a refused connection can be named as such.
type transportCapture struct {
	inner usecase.AppRunner
	base  string
	err   error
}

func (t *transportCapture) Do(c domain.AppCase) (domain.AppCase, error) {
	out, err := t.inner.Do(c)
	if err != nil && t.err == nil {
		t.err = profadapter.Unreachable(t.base, err)
	}
	return out, err
}

func finished(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}
