package wiregen

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-anything/go-anything/internal/adapter/app"
	"github.com/go-anything/go-anything/internal/domain"
)

// startLiveApp builds the whole module, reads the address main.go listens on, and starts the program
// there. It is the shared start of every check that must ask the assembled application (routing,
// middleware and auth included) instead of the unit's handler, so a run starts the program once per
// session rather than once per case. The returned stop kills the program and removes the build
// directory; the returned exited channel closes when the program exits. There is no free-port pre-check:
// the caller owns the process this starts, and StartAppOwned reports an exit. If something else already
// holds the address, the program exits on bind and the start fails there.
func startLiveApp(root, unit, originalURL string) (string, func(), <-chan struct{}, error) {
	dir, err := os.MkdirTemp("", "go-anything-live-*")
	if err != nil {
		return "", nil, nil, err
	}
	bin := filepath.Join(dir, "app")
	if err := BuildApp(root, bin); err != nil {
		os.RemoveAll(dir)
		return "", nil, nil, err
	}
	addr, err := ListenAddress(root, rbacMainFile)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, nil, err
	}
	stop, exited, err := StartAppOwned(bin, UnitEnv(unit, "", originalURL), addr)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, nil, err
	}
	cleanup := func() {
		// The program dies before its build directory goes: a running binary is locked on Windows.
		stop()
		os.RemoveAll(dir)
	}
	return addr, cleanup, exited, nil
}

// RunHTTPProbesLive sends HTTP convergence probes to the assembled application rather than to the unit's
// handler. The whole module is built, so main.go's routing, middleware and auth are included. The program is
// started on the address main.go listens on, each probe is sent over the network exactly as recorded, and the
// program is stopped. A probe that cannot be sent is an error, never an answer. Answers are returned in order.
func RunHTTPProbesLive(root, unit, originalURL string, probes []domain.AppCase) ([]domain.AppCase, error) {
	if len(probes) == 0 {
		return nil, nil
	}
	addr, stop, exited, err := startLiveApp(root, unit, originalURL)
	if err != nil {
		return nil, err
	}
	defer stop()
	runner := app.Runner{BaseURL: "http://" + addr}
	out := make([]domain.AppCase, len(probes))
	for i, p := range probes {
		// Check before each probe: a program that exited can no longer answer for itself, so any reply now
		// would come from a different process on the address (issue #17).
		select {
		case <-exited:
			return nil, fmt.Errorf("wire: the program exited before probe %d, so its answers cannot be trusted", i)
		default:
		}
		got, err := runner.Do(p)
		if err != nil {
			return nil, fmt.Errorf("wire: live probe %s %s: %w", p.Method, p.Path, err)
		}
		out[i] = withProbe(p, domain.AppCase{Status: got.Status, ResponseBody: got.ResponseBody, Headers: got.Headers})
		select {
		case <-exited:
			return nil, fmt.Errorf("wire: the program exited during the live probes, so its answers cannot be trusted")
		default:
		}
	}
	return out, nil
}
