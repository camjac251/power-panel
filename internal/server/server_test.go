package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/camjac251/power-panel/internal/bmc"
	"github.com/camjac251/power-panel/internal/config"
	"github.com/camjac251/power-panel/internal/db"
)

func TestTransitionForAction(t *testing.T) {
	tests := []struct {
		action string
		want   bmc.PowerState
	}{
		{"power_on", bmc.PoweringOn},
		{"power_off", bmc.ShuttingDown},
		{"power_forceoff", bmc.ShuttingDown},
		{"power_cycle", bmc.Restarting},
		{"power_reset", bmc.Restarting},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			got := transitionForAction(tt.action)
			if got != tt.want {
				t.Errorf("transitionForAction(%q) = %q, want %q", tt.action, got, tt.want)
			}
		})
	}
}

func TestTailscaleUser(t *testing.T) {
	// Headers from loopback (Tailscale Serve proxy) should be trusted
	r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Tailscale-User-Login", "cam@example.com")
	r.Header.Set("Tailscale-User-Name", "Cam")

	login, name := tailscaleUser(r)
	if login != "cam@example.com" {
		t.Errorf("login = %q, want %q", login, "cam@example.com")
	}
	if name != "Cam" {
		t.Errorf("name = %q, want %q", name, "Cam")
	}

	// Headers from non-loopback should be ignored (spoofing protection)
	r2 := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r2.RemoteAddr = "192.168.1.100:12345"
	r2.Header.Set("Tailscale-User-Login", "spoofed@example.com")
	r2.Header.Set("Tailscale-User-Name", "Spoofed")

	login2, name2 := tailscaleUser(r2)
	if login2 == "spoofed@example.com" {
		t.Error("non-loopback request: identity headers should not be trusted")
	}
	_ = name2

	// No headers from loopback
	r3 := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	r3.RemoteAddr = "127.0.0.1:12345"
	login3, name3 := tailscaleUser(r3)
	if login3 != "" || name3 != "" {
		t.Errorf("expected empty strings, got %q, %q", login3, name3)
	}
}

func TestRequireHTMX(t *testing.T) {
	s := &Server{}
	handler := s.requireHTMX(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Without HX-Request header
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/power/on", http.NoBody)
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("without HX-Request: status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	// With HX-Request header
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/power/on", http.NoBody)
	req.Header.Set("HX-Request", "true")
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("with HX-Request: status = %d, want %d", rec.Code, http.StatusOK)
	}

	// With wrong HX-Request value
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/power/on", http.NoBody)
	req.Header.Set("HX-Request", "false")
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("with HX-Request=false: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestCheckCooldown(t *testing.T) {
	s := &Server{
		cfg: &config.Config{
			Power: config.PowerConfig{
				Cooldown: 1 * time.Second,
			},
		},
	}

	// First call should succeed
	if err := s.checkCooldown(); err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}

	// Immediate second call should fail
	if err := s.checkCooldown(); err == nil {
		t.Fatal("second call: expected cooldown error")
	}

	// Backdate lastAction to bypass cooldown without sleeping
	s.mu.Lock()
	s.lastAction = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()
	if err := s.checkCooldown(); err != nil {
		t.Fatalf("after cooldown: unexpected error: %v", err)
	}
}

func testServer() *Server {
	bmcClient := bmc.NewClient(config.IPMIConfig{})
	return &Server{
		cfg:      &config.Config{Power: config.PowerConfig{BootTimeout: 2 * time.Minute, PollInterval: 5 * time.Second}},
		bmc:      bmcClient,
		bmcCache: bmc.NewCache(bmcClient, 5*time.Second),
		notifyCh: make(chan struct{}),
	}
}

func TestTransitionGetSetClear(t *testing.T) {
	s := testServer()

	// Initially empty
	if tc := s.getTransition(); tc.State != "" {
		t.Errorf("initial transition = %q, want empty", tc.State)
	}

	// Set and get
	s.setTransition(bmc.PoweringOn, "/redfish/v1/TaskService/Tasks/1", "power_on", "tester")
	tc := s.getTransition()
	if tc.State != bmc.PoweringOn {
		t.Errorf("after set: transition = %q, want %q", tc.State, bmc.PoweringOn)
	}
	if tc.TaskURL != "/redfish/v1/TaskService/Tasks/1" {
		t.Errorf("after set: task URL = %q, want %q", tc.TaskURL, "/redfish/v1/TaskService/Tasks/1")
	}
	if tc.Action != "power_on" {
		t.Errorf("after set: action = %q, want %q", tc.Action, "power_on")
	}
	if tc.User != "tester" {
		t.Errorf("after set: user = %q, want %q", tc.User, "tester")
	}

	// Clear and get
	s.clearTransition()
	if tc := s.getTransition(); tc.State != "" {
		t.Errorf("after clear: transition = %q, want empty", tc.State)
	}
}

func TestTransitionExpiry(t *testing.T) {
	s := testServer()

	// Set transition with backdated time
	s.transitionMu.Lock()
	s.transition = bmc.PoweringOn
	s.transitionTime = time.Now().Add(-3 * time.Minute)
	s.transitionMu.Unlock()

	// Should auto-clear after 2 minutes
	if tc := s.getTransition(); tc.State != "" {
		t.Errorf("expired transition = %q, want empty", tc.State)
	}
}

// flushRecorder wraps httptest.ResponseRecorder to implement http.Flusher.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed int
}

func (f *flushRecorder) Flush() { f.flushed++ }

func TestSSERendersStatusEvents(t *testing.T) {
	// Pre-seed cache with known BMC state
	bmcClient := bmc.NewClient(config.IPMIConfig{})
	cache := bmc.NewCache(bmcClient, time.Hour) // long TTL so it doesn't try to refresh

	// Seed the cache by calling Get (will fail since no real BMC, giving PowerUnknown)
	// Then we can test the SSE rendering path
	s := &Server{
		cfg: &config.Config{
			Power: config.PowerConfig{
				BootTimeout:  2 * time.Minute,
				PollInterval: 5 * time.Second,
			},
		},
		bmc:      bmcClient,
		bmcCache: cache,
		notifyCh: make(chan struct{}),
	}

	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/api/sse", http.NoBody)

	prevState, err := s.sendStatusEventWithTransition(rec, rec, req, "")
	if err != nil {
		t.Fatalf("sendStatusEventWithTransition failed: %v", err)
	}

	body := rec.Body.String()

	// Should contain SSE event markers
	if !strings.Contains(body, "event: status") {
		t.Error("missing 'event: status' in SSE output")
	}
	if !strings.Contains(body, "event: power") {
		t.Error("missing 'event: power' in SSE output")
	}
	if !strings.Contains(body, "event: actions") {
		t.Error("missing 'event: actions' in SSE output")
	}

	// Should have flushed
	if rec.flushed == 0 {
		t.Error("expected Flush to be called")
	}

	// With no BMC, state should be Unknown
	if prevState != bmc.PowerUnknown {
		t.Errorf("prevState = %q, want %q", prevState, bmc.PowerUnknown)
	}
}

func TestSSETransitionOverride(t *testing.T) {
	s := testServer()

	// Set a transition state
	s.setTransition(bmc.PoweringOn, "", "power_on", "tester")

	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/api/sse", http.NoBody)

	prevState, err := s.sendStatusEventWithTransition(rec, rec, req, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should show the transition state, not the real BMC state
	if prevState != bmc.PoweringOn {
		t.Errorf("during transition: prevState = %q, want %q", prevState, bmc.PoweringOn)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: status") {
		t.Error("missing status event during transition")
	}
}

func TestPowerActionHandler(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.Open(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer store.Close()

	bmcClient := bmc.NewClient(config.IPMIConfig{})
	s := &Server{
		cfg: &config.Config{
			Power: config.PowerConfig{
				Cooldown:     1 * time.Second,
				BootTimeout:  2 * time.Minute,
				PollInterval: 5 * time.Second,
			},
		},
		store:    store,
		bmc:      bmcClient,
		bmcCache: bmc.NewCache(bmcClient, 5*time.Second),
		mux:      http.NewServeMux(),
		notifyCh: make(chan struct{}),
	}

	// Test successful power action
	called := false
	handler := s.handlePowerAction("power_on", func() (string, error) {
		called = true
		return "", nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/power/on", http.NoBody)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("HX-Request", "true")
	handler(rec, req)

	if !called {
		t.Error("power action function was not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("HX-Trigger") != "refreshEvents" {
		t.Error("missing HX-Trigger: refreshEvents")
	}

	// Verify event was logged
	events, err := store.RecentEvents(req.Context(), 10)
	if err != nil {
		t.Fatalf("failed to get events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Action != "power_on" || !events[0].Success {
		t.Errorf("event = %+v, want action=power_on, success=true", events[0])
	}
}

func TestPowerActionCooldown(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.Open(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer store.Close()

	bmcClient := bmc.NewClient(config.IPMIConfig{})
	s := &Server{
		cfg: &config.Config{
			Power: config.PowerConfig{
				Cooldown:     10 * time.Second,
				BootTimeout:  2 * time.Minute,
				PollInterval: 5 * time.Second,
			},
		},
		store:    store,
		bmc:      bmcClient,
		bmcCache: bmc.NewCache(bmcClient, 5*time.Second),
		mux:      http.NewServeMux(),
		notifyCh: make(chan struct{}),
	}

	handler := s.handlePowerAction("power_on", func() (string, error) { return "", nil })

	// First request succeeds
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/power/on", http.NoBody)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("HX-Request", "true")
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("first request: status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Second request should hit cooldown
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/power/on", http.NoBody)
	req2.RemoteAddr = "127.0.0.1:12345"
	req2.Header.Set("HX-Request", "true")
	handler(rec2, req2)

	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("cooldown request: status = %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
}

func TestPowerActionFailure(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := db.Open(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer store.Close()

	bmcClient := bmc.NewClient(config.IPMIConfig{})
	s := &Server{
		cfg: &config.Config{
			Power: config.PowerConfig{
				Cooldown:     0,
				BootTimeout:  2 * time.Minute,
				PollInterval: 5 * time.Second,
			},
		},
		store:    store,
		bmc:      bmcClient,
		bmcCache: bmc.NewCache(bmcClient, 5*time.Second),
		mux:      http.NewServeMux(),
		notifyCh: make(chan struct{}),
	}

	handler := s.handlePowerAction("power_off", func() (string, error) {
		return "", fmt.Errorf("BMC connection refused")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/power/off", http.NoBody)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("HX-Request", "true")
	handler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	// Verify failure was logged
	events, err := store.RecentEvents(req.Context(), 10)
	if err != nil {
		t.Fatalf("failed to get events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Success {
		t.Error("expected event success=false for failed action")
	}
	if events[0].ErrorMsg == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := &Server{
		cfg: &config.Config{},
		mux: http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /api/health", s.handleHealth)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody)
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
}
