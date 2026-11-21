package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-anything/go-anything/internal/adapter/frontdoorgen"
	"github.com/go-anything/go-anything/internal/adapter/store"
)

const frontdoorScaffoldUsage = `frontdoor scaffold: usage: go-anything frontdoor scaffold <repo> -cases app.json -original-cmd "python app.py" [-port-env PORT] [-listen 127.0.0.1:8080] [-module app] [-check=true]`

// cmdFrontdoorScaffold writes the Go front door of a program in any language at <repo>/go/app: a Go
// main that owns the entry point while every request still reaches the original. The recorded cases
// decide between a server (http cases) and a command-line program (cli cases). Unless -check=false, it
// then builds the front door, runs it, and replays the cases through it: a fresh front door passes them
// all with every one answered by the original.
func cmdFrontdoorScaffold(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("frontdoor scaffold", flag.ContinueOnError)
	cases := fs.String("cases", "", "app recording made by `app record` (required)")
	originalCmd := fs.String("original-cmd", "", "command that runs the original, split like shell words but run without a shell (required); for a CLI, the program plus fixed leading arguments")
	portEnv := fs.String("port-env", frontdoorgen.DefaultPortEnv, "server: environment variable that tells the original its port")
	listen := fs.String("listen", frontdoorgen.DefaultListen, "server: address the front door listens on")
	module := fs.String("module", frontdoorgen.DefaultModule, "Go module path of the front door")
	check := fs.Bool("check", true, "build the front door and replay the recorded cases through it")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(frontdoorScaffoldUsage)
	}
	if *cases == "" {
		return errors.New("frontdoor scaffold: -cases is required\n" + frontdoorScaffoldUsage)
	}
	if *originalCmd == "" {
		return errors.New("frontdoor scaffold: -original-cmd is required\n" + frontdoorScaffoldUsage)
	}
	rec, err := store.LoadApp(*cases)
	if err != nil {
		return err
	}
	if _, err := frontdoorgen.DetectKind(rec.Cases); err != nil {
		return fmt.Errorf("frontdoor scaffold: %s: %w", *cases, err)
	}
	res, err := frontdoorgen.Scaffold(pos[0], frontdoorgen.Options{
		Cases: rec.Cases, OriginalCmd: *originalCmd, PortEnv: *portEnv, Listen: *listen, Module: *module,
	})
	if err != nil {
		return err
	}
	for _, p := range res.Files {
		fmt.Fprintf(stdout, "wrote %s\n", p)
	}
	kindText := "command-line front door: runs %q with the same arguments, input, output and environment\n"
	if res.Kind == frontdoorgen.KindServer {
		kindText = "server front door: starts %q on a free port and proxies every route to it\n"
	}
	fmt.Fprintf(stdout, kindText, res.Original)
	if !*check {
		fmt.Fprintln(stdout, "check skipped (-check=false): the front door was not built or run")
		return nil
	}

	// Ctrl-C during the check still stops the processes it started.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := frontdoorgen.Check(ctx, res, rec)
	if rep.Total > 0 {
		fmt.Fprintf(stdout, "check: %d of %d recorded case(s) passed through the front door; %d answered by the original (fallback)\n",
			rep.Passed, rep.Total, rep.Fallbacks)
		for _, m := range rep.Mismatches {
			fmt.Fprintf(stdout, "  %s: %s differs: want %q, got %q\n", m.Case, m.What, m.Want, m.Got)
		}
	}
	if res.Kind == frontdoorgen.KindServer && rep.ListenAddr != "" {
		fmt.Fprintf(stdout, "check: front door %s, original %s, both ports closed after stop: %v\n", rep.ListenAddr, rep.OriginalAddr, rep.PortsClosed)
	}
	if err != nil {
		return fmt.Errorf("frontdoor scaffold: the front door was written to %s but the check failed: %w", res.Dir, err)
	}
	if !rep.AllPassed() {
		return fmt.Errorf("frontdoor scaffold: %d of %d case(s) differ through the front door", rep.Total-rep.Passed, rep.Total)
	}
	if rep.Fallbacks != int64(rep.Total) {
		fmt.Fprintf(stdout, "note: %d fallback(s) for %d case(s); a fresh front door sends every case to the original\n", rep.Fallbacks, rep.Total)
	}
	fmt.Fprintf(stdout, "next: go-anything wire gen %s <unit> [prefix] moves one unit into Go\n", res.Dir)
	return nil
}
