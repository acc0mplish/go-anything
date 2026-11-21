// Package frontdoor is the HTTP adapter that routes each request to the rewrite or the original.
//
// A request routed to the rewrite is never silently sent to the original when the rewrite is
// down: the caller gets a 502. A silent fallback would make a broken rewrite look healthy, so the
// migration would appear to succeed while the new code is not running at all.
package frontdoor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/routesfile"
	"github.com/go-anything/go-anything/internal/domain"
)

// Handler forwards requests according to its routes. When it was built from a routes file, the file
// is checked on every request and reloaded when it changes, so `wire` takes effect without a restart.
type Handler struct {
	mu       sync.Mutex
	Routes   []domain.Route
	fileName string
	fileMod  time.Time
	Original *url.URL // the existing program
	Rewrite  *url.URL // the new program; nil means no rewrite is wired yet
	Client   *http.Client
}

// New validates the routes and URLs and returns a ready handler.
func New(routes []domain.Route, original, rewrite string) (*Handler, error) {
	if err := domain.ValidRoutes(routes); err != nil {
		return nil, err
	}
	o, err := parseBase(original)
	if err != nil {
		return nil, fmt.Errorf("frontdoor: original: %w", err)
	}
	h := &Handler{Routes: routes, Original: o, Client: &http.Client{Timeout: 30 * time.Second}}
	if rewrite != "" {
		rw, err := parseBase(rewrite)
		if err != nil {
			return nil, fmt.Errorf("frontdoor: rewrite: %w", err)
		}
		h.Rewrite = rw
	}
	for _, r := range routes {
		if r.Target == domain.TargetRewrite && h.Rewrite == nil {
			return nil, fmt.Errorf("frontdoor: a route sends traffic to the rewrite but no rewrite URL was given")
		}
	}
	return h, nil
}

func parseBase(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("must be http or https, got %q", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host in %q", raw)
	}
	return u, nil
}

// NewFromFile builds a handler whose routes come from a routing file that is reloaded on change.
// The file must exist and be valid at start; later bad edits keep the last good routes.
func NewFromFile(path, original, rewrite string) (*Handler, error) {
	routes, err := routesfile.Load(path)
	if err != nil {
		return nil, err
	}
	h, err := New(routes, original, rewrite)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("frontdoor: stat routes: %w", err)
	}
	h.fileName = path
	h.fileMod = fi.ModTime()
	return h, nil
}

// currentRoutes returns the routes to use for this request, reloading the file when it changed.
func (h *Handler) currentRoutes() []domain.Route {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fileName == "" {
		return h.Routes
	}
	fi, err := os.Stat(h.fileName)
	if err != nil || fi.ModTime().Equal(h.fileMod) {
		return h.Routes
	}
	// Mark the change as seen even when it is invalid, so one bad edit is reported once, not per request.
	h.fileMod = fi.ModTime()
	routes, err := routesfile.Load(h.fileName)
	if err == nil {
		for _, r := range routes {
			if r.Target == domain.TargetRewrite && h.Rewrite == nil {
				err = errors.New("a route sends traffic to the rewrite but no rewrite URL was given")
				break
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "frontdoor: routes file changed but is invalid, keeping previous routes: %v\n", err)
		return h.Routes
	}
	h.Routes = routes
	return h.Routes
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := domain.Decide(h.currentRoutes(), r.Method, r.URL.Path)
	base := h.Original
	if target == domain.TargetRewrite {
		base = h.Rewrite
	}
	out, err := h.forward(base, r)
	if err != nil {
		if target == domain.TargetRewrite {
			http.Error(w, "rewrite unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		http.Error(w, "original unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer out.Body.Close()
	for k, vs := range out.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	// Tell the caller who answered; useful while the migration is in progress.
	w.Header().Set("X-Served-By", target)
	w.WriteHeader(out.StatusCode)
	io.Copy(w, out.Body)
}

func (h *Handler) forward(base *url.URL, r *http.Request) (*http.Response, error) {
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + r.URL.Path
	u.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), r.Body)
	if err != nil {
		return nil, err
	}
	req.Header = r.Header.Clone()
	return h.Client.Do(req)
}
