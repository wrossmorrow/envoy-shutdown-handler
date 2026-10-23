package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newEnvoyMock spins up an httptest server emulating the small subset of the
// envoy admin interface used by this handler and points the package-level
// adminBaseUrl at it.
func newEnvoyMock(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	adminBaseUrl = srv.URL
	return srv
}

// statsHandler responds to envoy stats queries with the given active
// connection count.
func statsHandler(count int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/stats" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "http.envoy.downstream_cx_active: %d", count)
	}
}

func TestFlagDefaults(t *testing.T) {
	expected := map[string]string{
		"shutdown-handler-port":  "9001",
		"envoy-admin-host":       "localhost",
		"envoy-admin-port":       "9901",
		"envoy-admin-scheme":     "http",
		"initial-delay-seconds":  "0",
		"check-period-seconds":   "5",
		"check-deadline-seconds": "300",
		"force":                  "false",
	}
	for name, want := range expected {
		if got := flag.Lookup(name).DefValue; got != want {
			t.Errorf("flag -%s default = %q, want %q", name, got, want)
		}
	}
}

func TestParseDownstreamCnx(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{"zero active connections", "http.envoy.downstream_cx_active: 0", 0, false},
		{"one active connection", "http.envoy.downstream_cx_active: 1", 1, false},
		{"multiple active connections", "http.envoy.downstream_cx_active:  2", 2, false},
		{"surrounding whitespace is tolerated", "   http.envoy.downstream_cx_active:  7   ", 7, false},
		{"among other stats entries", "http.foo.bar: 1\nhttp.envoy.downstream_cx_active: 9", 9, false},
		{"empty body", "", -1, true},
		{"missing stats key", "http.foo.bar: 1", -1, true},
		{"non numeric value", "http.envoy.downstream_cx_active: abc", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDownstreamCnx(tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseDownstreamCnx(%q) error = %v, wantErr %v", tt.body, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseDownstreamCnx(%q) = %d, want %d", tt.body, got, tt.want)
			}
		})
	}
}

func TestFailEnvoyHealthCheck(t *testing.T) {
	t.Run("envoy accepts health check failure", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/healthcheck/fail" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		if err := failEnvoyHealthCheck(); err != nil {
			t.Errorf("failEnvoyHealthCheck() error = %v, want nil", err)
		}
	})

	t.Run("envoy rejects health check failure", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		if err := failEnvoyHealthCheck(); err == nil {
			t.Error("failEnvoyHealthCheck() error = nil, want error")
		}
	})

	t.Run("envoy unreachable", func(t *testing.T) {
		adminBaseUrl = "http://127.0.0.1:1" // nothing listening here
		if err := failEnvoyHealthCheck(); err == nil {
			t.Error("failEnvoyHealthCheck() error = nil, want error")
		}
	})
}

func TestStartGracefulDraining(t *testing.T) {
	t.Run("envoy accepts graceful drain", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/drain_listeners" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.URL.Query().Get("graceful") != "true" && r.URL.Query().Get("graceful") != "" {
				// graceful is a bare flag; just accept any value
			}
			w.WriteHeader(http.StatusOK)
		})

		if err := startGracefulDraining(); err != nil {
			t.Errorf("startGracefulDraining() error = %v, want nil", err)
		}
	})

	t.Run("envoy rejects graceful drain", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		if err := startGracefulDraining(); err == nil {
			t.Error("startGracefulDraining() error = nil, want error")
		}
	})

	t.Run("envoy unreachable", func(t *testing.T) {
		adminBaseUrl = "http://127.0.0.1:1"
		if err := startGracefulDraining(); err == nil {
			t.Error("startGracefulDraining() error = nil, want error")
		}
	})
}

func TestForceEnvoyShutdown(t *testing.T) {
	t.Run("envoy accepts force shutdown", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/quitquitquit" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		if err := forceEnvoyShutdown(); err != nil {
			t.Errorf("forceEnvoyShutdown() error = %v, want nil", err)
		}
	})

	t.Run("envoy rejects force shutdown", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		if err := forceEnvoyShutdown(); err == nil {
			t.Error("forceEnvoyShutdown() error = nil, want error")
		}
	})

	t.Run("envoy unreachable", func(t *testing.T) {
		adminBaseUrl = "http://127.0.0.1:1"
		if err := forceEnvoyShutdown(); err == nil {
			t.Error("forceEnvoyShutdown() error = nil, want error")
		}
	})
}

func TestDefaultIntFromQuery(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		def     int
		want    int
		wantErr bool
	}{
		{"missing key uses default", "/shutdown", 7, 7, false},
		{"empty value uses default", "/shutdown?delay=", 7, 7, false},
		{"valid value", "/shutdown?delay=3", 7, 3, false},
		{"invalid value uses default and errors", "/shutdown?delay=abc", 7, 7, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			got, err := defaultIntFromQuery(r, "delay", tt.def)
			if (err != nil) != tt.wantErr {
				t.Fatalf("defaultIntFromQuery(%q, %d) error = %v, wantErr %v", tt.path, tt.def, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("defaultIntFromQuery(%q, %d) = %d, want %d", tt.path, tt.def, got, tt.want)
			}
		})
	}
}

func TestDefaultNonNegIntFromQuery(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		def     int
		want    int
		wantErr bool
	}{
		{"missing key uses default", "/shutdown", 7, 7, false},
		{"valid value", "/shutdown?delay=2", 7, 2, false},
		{"zero is allowed", "/shutdown?delay=0", 7, 0, false},
		{"negative value uses default and errors", "/shutdown?delay=-1", 7, 7, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			got, err := defaultNonNegIntFromQuery(r, "delay", tt.def)
			if (err != nil) != tt.wantErr {
				t.Fatalf("defaultNonNegIntFromQuery(%q, %d) error = %v, wantErr %v", tt.path, tt.def, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("defaultNonNegIntFromQuery(%q, %d) = %d, want %d", tt.path, tt.def, got, tt.want)
			}
		})
	}
}

func TestParseCommonQueryParams(t *testing.T) {
	origDelay, origPeriod, origDeadline := *delay, *period, *deadline
	defer func() {
		*delay, *period, *deadline = origDelay, origPeriod, origDeadline
	}()

	t.Run("missing params use flag defaults", func(t *testing.T) {
		*delay, *period, *deadline = 3, 5, 10

		r := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
		d, p, dl, err := parseCommonQueryParams(r)
		if err != nil {
			t.Fatalf("parseCommonQueryParams() error = %v, want nil", err)
		}
		if d != 3 || p != 5 || dl != 10 {
			t.Errorf("parseCommonQueryParams() = (%d, %d, %d), want (3, 5, 10)", d, p, dl)
		}
	})

	t.Run("params override flag defaults", func(t *testing.T) {
		*delay, *period, *deadline = 3, 5, 10

		r := httptest.NewRequest(http.MethodGet, "/shutdown?delay=1&period=2&deadline=9", nil)
		d, p, dl, err := parseCommonQueryParams(r)
		if err != nil {
			t.Fatalf("parseCommonQueryParams() error = %v, want nil", err)
		}
		if d != 1 || p != 2 || dl != 9 {
			t.Errorf("parseCommonQueryParams() = (%d, %d, %d), want (1, 2, 9)", d, p, dl)
		}
	})

	t.Run("invalid param returns error", func(t *testing.T) {
		*delay, *period, *deadline = 3, 5, 10

		r := httptest.NewRequest(http.MethodGet, "/shutdown?delay=-1", nil)
		d, p, dl, err := parseCommonQueryParams(r)
		if err == nil {
			t.Error("parseCommonQueryParams() error = nil, want error for negative value")
		}
		if d != -1 || p != -1 || dl != -1 {
			t.Errorf("parseCommonQueryParams() on error = (%d, %d, %d), want (-1, -1, -1)", d, p, dl)
		}
	})
}

func TestAliveHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/health/alive", nil)
	alive(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("alive() status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "OK" {
		t.Errorf("alive() body = %q, want %q", got, "OK")
	}
}

func TestReadyHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	ready(rec, r)

	if rec.Code != http.StatusOK {
		t.Errorf("ready() status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "OK" {
		t.Errorf("ready() body = %q, want %q", got, "OK")
	}
}

func TestCheckStatsHandler(t *testing.T) {
	t.Run("envoy reports active connections", func(t *testing.T) {
		newEnvoyMock(t, statsHandler(4))

		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/check/stats", nil)
		checkStats(rec, r)

		if rec.Code != http.StatusOK {
			t.Fatalf("checkStats() status = %d, want %d", rec.Code, http.StatusOK)
		}
		if !strings.Contains(rec.Body.String(), "4") {
			t.Errorf("checkStats() body = %q, want it to contain the connection count", rec.Body.String())
		}
	})

	t.Run("envoy errors", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/check/stats", nil)
		checkStats(rec, r)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("checkStats() status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})

	t.Run("envoy unreachable", func(t *testing.T) {
		adminBaseUrl = "http://127.0.0.1:1"
		defer func() { adminBaseUrl = "" }()

		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/check/stats", nil)
		checkStats(rec, r)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("checkStats() status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})
}

func TestShutdownHandler(t *testing.T) {
	t.Run("invalid delay returns bad request", func(t *testing.T) {
		complete = nil
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/shutdown?delay=-1", nil)
		shutdown(rec, r)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("shutdown() status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("deadline too short returns bad request", func(t *testing.T) {
		complete = nil
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/shutdown?delay=10&period=5&deadline=14", nil)
		shutdown(rec, r)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("shutdown() status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("health check failure returns internal server error", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/healthcheck/fail" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})

		complete = make(chan bool, 1)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
		shutdown(rec, r)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("shutdown() status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if got := <-complete; got {
			t.Error("shutdown() completed signal = true, want false")
		}
	})

	t.Run("stats failure returns internal server error", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/stats" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		})

		complete = make(chan bool, 1)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
		shutdown(rec, r)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("shutdown() status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if got := <-complete; got {
			t.Error("shutdown() completed signal = true, want false")
		}
	})

	t.Run("no active connections shuts down immediately", func(t *testing.T) {
		drainCalled := false
		forceCalled := false
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/healthcheck/fail":
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && r.URL.Path == "/stats":
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "http.envoy.downstream_cx_active: %d", 0)
			case r.Method == http.MethodPost && r.URL.Path == "/drain_listeners":
				drainCalled = true
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodPost && r.URL.Path == "/quitquitquit":
				forceCalled = true
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		*force = true
		complete = make(chan bool, 1)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
		shutdown(rec, r)

		if rec.Code != http.StatusOK {
			t.Errorf("shutdown() status = %d, want %d", rec.Code, http.StatusOK)
		}
		if drainCalled {
			t.Error("shutdown() called drain_listeners, want no drain with zero active connections")
		}
		if forceCalled {
			t.Error("shutdown() called force shutdown, want no force with zero active connections")
		}
		if got := <-complete; !got {
			t.Error("shutdown() completed signal = false, want true")
		}
	})

	t.Run("drains until connections close then force shuts down", func(t *testing.T) {
		statsCalls := 0
		drainCalled := false
		forceCalled := false
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/healthcheck/fail":
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && r.URL.Path == "/stats":
				statsCalls++
				if statsCalls == 1 {
					w.WriteHeader(http.StatusOK)
					fmt.Fprintf(w, "http.envoy.downstream_cx_active: %d", 1)
				} else {
					w.WriteHeader(http.StatusOK)
					fmt.Fprintf(w, "http.envoy.downstream_cx_active: %d", 0)
				}
			case r.Method == http.MethodPost && r.URL.Path == "/drain_listeners":
				drainCalled = true
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodPost && r.URL.Path == "/quitquitquit":
				forceCalled = true
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		*force = true
		complete = make(chan bool, 1)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
		shutdown(rec, r)

		if rec.Code != http.StatusOK {
			t.Errorf("shutdown() status = %d, want %d", rec.Code, http.StatusOK)
		}
		if !drainCalled {
			t.Error("shutdown() did not call drain_listeners, want drain to start")
		}
		if !forceCalled {
			t.Error("shutdown() did not call force shutdown, want force after connections closed")
		}
		if got := <-complete; !got {
			t.Error("shutdown() completed signal = false, want true")
		}
	})

	t.Run("shutdown timeout returns request timeout", func(t *testing.T) {
		newEnvoyMock(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/healthcheck/fail":
				w.WriteHeader(http.StatusOK)
			case r.Method == http.MethodGet && r.URL.Path == "/stats":
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "http.envoy.downstream_cx_active: %d", 1)
			case r.Method == http.MethodPost && r.URL.Path == "/drain_listeners":
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})

		complete = make(chan bool, 1)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/shutdown?delay=0&period=0&deadline=1", nil)
		shutdown(rec, r)

		if rec.Code != http.StatusRequestTimeout {
			t.Errorf("shutdown() status = %d, want %d", rec.Code, http.StatusRequestTimeout)
		}
		if got := <-complete; !got {
			t.Error("shutdown() completed signal = true, want true (timeout still signals completion)")
		}
	})
}

func TestWaitForShutdownHandler(t *testing.T) {
	t.Run("success when shutdown completed", func(t *testing.T) {
		complete = make(chan bool, 1)
		complete <- true
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/waitforshutdown", nil)
		waitForShutdown(rec, r)

		if rec.Code != http.StatusOK {
			t.Errorf("waitForShutdown() status = %d, want %d", rec.Code, http.StatusOK)
		}
		if !strings.Contains(rec.Body.String(), "shutdown completed") {
			t.Errorf("waitForShutdown() body = %q, want it to contain the completion message", rec.Body.String())
		}
		if complete != nil {
			t.Error("waitForShutdown() did not reset complete channel to nil")
		}
	})

	t.Run("failure when shutdown failed", func(t *testing.T) {
		complete = make(chan bool, 1)
		complete <- false
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/waitforshutdown", nil)
		waitForShutdown(rec, r)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("waitForShutdown() status = %d, want %d", rec.Code, http.StatusInternalServerError)
		}
		if !strings.Contains(rec.Body.String(), "an error occurred during shutdown") {
			t.Errorf("waitForShutdown() body = %q, want it to contain the failure message", rec.Body.String())
		}
		if complete != nil {
			t.Error("waitForShutdown() did not reset complete channel to nil")
		}
	})

	t.Run("timeout when shutdown does not complete", func(t *testing.T) {
		complete = nil
		origPeriod, origDeadline := *period, *deadline
		defer func() { *period, *deadline = origPeriod, origDeadline }()
		*period = 0
		*deadline = 1

		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/waitforshutdown", nil)
		waitForShutdown(rec, r)

		if rec.Code != http.StatusRequestTimeout {
			t.Errorf("waitForShutdown() status = %d, want %d", rec.Code, http.StatusRequestTimeout)
		}
	})

	t.Run("invalid params return bad request", func(t *testing.T) {
		complete = nil
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/waitforshutdown?delay=-1", nil)
		waitForShutdown(rec, r)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("waitForShutdown() status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}
