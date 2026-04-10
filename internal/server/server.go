package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/camjac251/power-panel/internal/bmc"
	"github.com/camjac251/power-panel/internal/config"
	"github.com/camjac251/power-panel/internal/db"
	"github.com/camjac251/power-panel/internal/updater"
	"github.com/camjac251/power-panel/views"
)

type Server struct {
	cfg        *config.Config
	store      *db.Store
	bmc        *bmc.Client
	bmcCache   *bmc.Cache
	wol        *bmc.WoLClient
	assets     fs.FS
	version    string
	updater    *updater.Updater // nil if in container or dev mode
	mux        *http.ServeMux
	lastAction time.Time
	mu         sync.Mutex

	// Transition state for immediate UI feedback
	transition        bmc.PowerState
	transitionTime    time.Time
	transitionTaskURL string // Redfish task URL for the in-flight power action
	transitionAction  string // logical action name (e.g. "power_off") for failure logging
	transitionUser    string // tailscale login that triggered the action
	transitionMu      sync.Mutex

	// Broadcast channel to wake SSE handlers immediately
	notifyCh chan struct{}
	notifyMu sync.Mutex
}

func New(cfg *config.Config, store *db.Store, bmcClient *bmc.Client, wolClient *bmc.WoLClient, assets fs.FS, version string, upd *updater.Updater) *Server {
	s := &Server{
		cfg:      cfg,
		store:    store,
		bmc:      bmcClient,
		bmcCache: bmc.NewCache(bmcClient, cfg.Power.PollInterval),
		wol:      wolClient,
		assets:   assets,
		version:  version,
		updater:  upd,
		mux:      http.NewServeMux(),
		notifyCh: make(chan struct{}),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Static assets
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(s.assets))))

	// Pages
	s.mux.HandleFunc("GET /{$}", s.handleHome)

	// API
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/sensors", s.handleSensors)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/health", s.handleHealth)

	// Power actions (CSRF-protected via HX-Request header check)
	s.mux.HandleFunc("POST /api/power/on", s.requireHTMX(s.handlePowerAction("power_on", s.powerOn)))
	s.mux.HandleFunc("POST /api/power/off", s.requireHTMX(s.handlePowerAction("power_off", s.bmc.GracefulShutdown)))
	s.mux.HandleFunc("POST /api/power/forceoff", s.requireHTMX(s.handlePowerAction("power_forceoff", s.bmc.PowerOff)))
	s.mux.HandleFunc("POST /api/power/cycle", s.requireHTMX(s.handlePowerAction("power_cycle", s.bmc.PowerCycle)))
	s.mux.HandleFunc("POST /api/power/reset", s.requireHTMX(s.handlePowerAction("power_reset", s.bmc.PowerCycle)))

	// HTML fragments (for htmx partial updates)
	s.mux.HandleFunc("GET /api/sensors/key", s.handleKeySensorsFragment)
	s.mux.HandleFunc("GET /api/sensors/all", s.handleAllSensorsFragment)
	s.mux.HandleFunc("GET /api/events/fragment", s.handleEventsFragment)

	// Update
	s.mux.HandleFunc("GET /api/update/status", s.handleUpdateStatus)
	s.mux.HandleFunc("POST /api/update/check", s.requireHTMX(s.handleUpdateCheck))
	s.mux.HandleFunc("POST /api/update/apply", s.requireHTMX(s.handleUpdateApply))

	// SSE
	s.mux.HandleFunc("GET /api/sse", s.handleSSE)
}

func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE needs unbounded writes
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic during HTTP shutdown", "error", r)
			}
		}()
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("unclean HTTP shutdown", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// powerOn uses Redfish first, then sends WoL as backup. Returns the Redfish
// task monitor URL (empty if Redfish failed) so the caller can poll for
// late-arriving exceptions reported by the BMC.
func (s *Server) powerOn() (string, error) {
	taskURL, redfishErr := s.bmc.PowerOn()
	if redfishErr != nil {
		slog.Warn("Redfish power on failed, sending WoL as fallback", "error", redfishErr)
	}
	// Always send WoL as backup (harmless if machine is already powering on)
	wolErr := s.wol.Wake(context.Background())
	if wolErr != nil {
		slog.Warn("WoL failed", "error", wolErr)
	}
	// Succeed if either method worked
	if redfishErr != nil && wolErr != nil {
		return "", redfishErr
	}
	return taskURL, nil
}

func (s *Server) checkCooldown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	elapsed := time.Since(s.lastAction)
	if elapsed < s.cfg.Power.Cooldown {
		remaining := s.cfg.Power.Cooldown - elapsed
		return fmt.Errorf("cooldown active, wait %d seconds", int(remaining.Seconds()))
	}
	s.lastAction = time.Now()
	return nil
}

func (s *Server) capabilities() views.Capabilities {
	return views.Capabilities{
		GracefulShutdown: s.bmc.SupportsReset("GracefulShutdown"),
		ForceOff:         s.bmc.SupportsReset("ForceOff"),
		ForceRestart:     s.bmc.SupportsReset("ForceRestart"),
		HasWebUI:         s.cfg.Server.BMCURL != "",
	}
}

// tailscaleUser identifies the requesting user. Only trusts Tailscale Serve
// identity headers from the loopback proxy. Falls back to the local Tailscale
// API (WhoIs over unix socket) for direct access.
func tailscaleUser(r *http.Request) (login, name string) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	isLocal := host == "127.0.0.1" || host == "::1"

	if isLocal {
		if login = r.Header.Get("Tailscale-User-Login"); login != "" {
			return login, r.Header.Get("Tailscale-User-Name")
		}
		// Through Serve, r.RemoteAddr is 127.0.0.1 (the proxy). Use X-Forwarded-For
		// for the real Tailscale IP.
		if addr := r.Header.Get("X-Forwarded-For"); addr != "" {
			return tailscaleWhoIs(r.Context(), addr)
		}
	}

	return tailscaleWhoIs(r.Context(), r.RemoteAddr)
}

var tsLocalClient = &http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/tailscale/tailscaled.sock")
		},
	},
}

func tailscaleWhoIs(ctx context.Context, addr string) (login, name string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://local-tailscaled.sock/localapi/v0/whois?addr="+url.QueryEscape(addr), http.NoBody)
	if err != nil {
		return "", ""
	}
	resp, err := tsLocalClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var who struct {
		Node struct {
			ComputedName string   `json:"ComputedName"`
			Tags         []string `json:"Tags"`
		} `json:"Node"`
		UserProfile struct {
			LoginName   string `json:"LoginName"`
			DisplayName string `json:"DisplayName"`
		} `json:"UserProfile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		return "", ""
	}
	// For non-tagged nodes, use the user profile
	if len(who.Node.Tags) == 0 && who.UserProfile.LoginName != "tagged-devices" {
		return who.UserProfile.LoginName, who.UserProfile.DisplayName
	}
	// For tagged nodes, use the device name
	if who.Node.ComputedName != "" {
		return who.Node.ComputedName, who.Node.ComputedName
	}
	return "", ""
}

// requireHTMX rejects requests without the HX-Request header (CSRF protection).
// htmx always sends this header; cross-origin form submissions cannot.
func (s *Server) requireHTMX(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HX-Request") != "true" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	// Render immediately with no BMC data. SSE fills in status and sensors;
	// htmx polling fills events. No blocking on BMC.
	events, err := s.store.RecentEvents(r.Context(), 10)
	if err != nil {
		slog.Error("failed to load recent events", "error", err)
	}

	data := views.HomeData{
		ServerName:   s.cfg.Server.Name,
		Description:  s.cfg.Server.Description,
		BMCURL:       s.cfg.Server.BMCURL,
		NoMachineURL: s.cfg.Server.NoMachineURL,
		Version:      s.version,
		BMCFirmware:  s.bmc.FirmwareVersion(),
		Status:       bmc.Status{PowerState: bmc.PowerUnknown},
		Events:       events,
		Caps:         s.capabilities(),
	}

	if err := views.Home(data).Render(r.Context(), w); err != nil {
		slog.Error("failed to render home", "error", err)
	}
}

func (s *Server) handleKeySensorsFragment(w http.ResponseWriter, r *http.Request) {
	sensors, err := s.bmc.GetSensors()
	if err != nil {
		_ = views.FragmentError("Failed to read sensors", "/api/sensors/key").Render(r.Context(), w)
		return
	}
	_ = views.KeySensors(sensors).Render(r.Context(), w)
}

func (s *Server) handleAllSensorsFragment(w http.ResponseWriter, r *http.Request) {
	sensors, err := s.bmc.GetSensors()
	if err != nil {
		_ = views.FragmentError("Failed to read sensors", "/api/sensors/all").Render(r.Context(), w)
		return
	}
	_ = views.AllSensors(sensors).Render(r.Context(), w)
}

func (s *Server) handleEventsFragment(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RecentEvents(r.Context(), 10)
	if err != nil {
		_ = views.FragmentError("Failed to load events", "/api/events/fragment").Render(r.Context(), w)
		return
	}
	_ = views.ActivityLog(events).Render(r.Context(), w)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.bmc.GetStatus()
	if err != nil {
		slog.Error("failed to get BMC status", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"power_state": "Unknown",
			"error":       "BMC communication failed",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"power_state": status.PowerState,
		"health":      status.Health,
		"server_name": s.cfg.Server.Name,
	})
}

func (s *Server) handleSensors(w http.ResponseWriter, r *http.Request) {
	data, err := s.bmc.GetSensors()
	if err != nil {
		slog.Error("failed to get sensors", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "BMC communication failed"})
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RecentEvents(r.Context(), 20)
	if err != nil {
		slog.Error("failed to load events", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load events"})
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// setTransition sets the transitioning power state and notifies SSE clients.
// taskURL is the Redfish task monitor URL returned by the BMC, used to detect
// late-arriving Exception states. action and user are recorded so SSE can log
// a failure event with proper attribution if the task fails.
func (s *Server) setTransition(state bmc.PowerState, taskURL, action, user string) {
	slog.Info("transition set", "state", state, "task_url", taskURL)
	s.transitionMu.Lock()
	s.transition = state
	s.transitionTime = time.Now()
	s.transitionTaskURL = taskURL
	s.transitionAction = action
	s.transitionUser = user
	s.transitionMu.Unlock()
	s.bmcCache.Invalidate()
	s.notifySSE()
}

// clearTransition removes the transitioning state and notifies SSE clients.
func (s *Server) clearTransition() {
	slog.Info("transition cleared")
	s.transitionMu.Lock()
	s.transition = ""
	s.transitionTaskURL = ""
	s.transitionAction = ""
	s.transitionUser = ""
	s.transitionMu.Unlock()
	s.notifySSE()
}

// transitionContext bundles transition state for SSE polling.
type transitionContext struct {
	State   bmc.PowerState
	Age     time.Duration
	TaskURL string
	Action  string
	User    string
}

// getTransition returns the current transition state along with its task URL
// and metadata. It clears the transition if it has aged past the boot timeout.
func (s *Server) getTransition() transitionContext {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if s.transition == "" {
		return transitionContext{}
	}
	age := time.Since(s.transitionTime)
	if age > s.cfg.Power.BootTimeout {
		s.transition = ""
		s.transitionTaskURL = ""
		s.transitionAction = ""
		s.transitionUser = ""
		return transitionContext{}
	}
	return transitionContext{
		State:   s.transition,
		Age:     age,
		TaskURL: s.transitionTaskURL,
		Action:  s.transitionAction,
		User:    s.transitionUser,
	}
}

// notifySSE wakes all SSE handlers to push an immediate update.
func (s *Server) notifySSE() {
	s.notifyMu.Lock()
	close(s.notifyCh)
	s.notifyCh = make(chan struct{})
	s.notifyMu.Unlock()
}

// getNotifyCh returns the current notification channel for SSE select.
func (s *Server) getNotifyCh() chan struct{} {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	return s.notifyCh
}

// transitionForAction maps a power action to its transitioning state.
func transitionForAction(action string) bmc.PowerState {
	switch action {
	case "power_on":
		return bmc.PoweringOn
	case "power_off":
		return bmc.ShuttingDown
	case "power_forceoff":
		return bmc.ShuttingDown
	case "power_cycle", "power_reset":
		return bmc.Restarting
	default:
		return ""
	}
}

func (s *Server) handlePowerAction(action string, fn func() (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkCooldown(); err != nil {
			w.Header().Set("HX-Retarget", "#toast-container")
			w.Header().Set("HX-Reswap", "beforeend")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = views.ToastError(err.Error()).Render(r.Context(), w)
			return
		}

		login, name := tailscaleUser(r)
		if login == "" {
			login = "unknown"
		}

		slog.Info("power action requested", "action", action, "user", login)

		var taskURL string
		dur, err := bmc.TimedCall(func() error {
			var callErr error
			taskURL, callErr = fn()
			return callErr
		})

		ev := db.PowerEvent{
			Action:     action,
			UserLogin:  login,
			UserName:   name,
			Success:    err == nil,
			DurationMS: dur.Milliseconds(),
		}
		if err != nil {
			ev.ErrorMsg = err.Error()
		}

		if logErr := s.store.LogEvent(r.Context(), ev); logErr != nil {
			slog.Error("failed to log event", "error", logErr)
		}

		// Return toast HTML for htmx to swap into #toast-container
		w.Header().Set("HX-Retarget", "#toast-container")
		w.Header().Set("HX-Reswap", "beforeend")
		// Trigger immediate event log refresh
		w.Header().Set("HX-Trigger", "refreshEvents")

		if err != nil {
			slog.Error("power action failed", "action", action, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = views.ToastError(fmt.Sprintf("%s failed", views.FormatAction(action))).Render(r.Context(), w)
			return
		}

		slog.Info("power action completed", "action", action, "user", login, "duration_ms", dur.Milliseconds(), "task_url", taskURL)

		if ts := transitionForAction(action); ts != "" {
			s.setTransition(ts, taskURL, action, login)
		}

		_ = views.ToastSuccess(fmt.Sprintf("%s command sent", views.FormatAction(action))).Render(r.Context(), w)
	}
}

func (s *Server) updateStatus() updater.UpdateStatus {
	if s.updater == nil {
		return updater.UpdateStatus{
			CurrentVersion: s.version,
			InContainer:    updater.InContainer(),
		}
	}
	return s.updater.Status()
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	_ = views.UpdateBanner(s.updateStatus()).Render(r.Context(), w)
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		_ = views.UpdateBanner(s.updateStatus()).Render(r.Context(), w)
		return
	}
	_ = s.updater.Check(r.Context())
	_ = views.UpdateBanner(s.updateStatus()).Render(r.Context(), w)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		_ = views.UpdateBanner(s.updateStatus()).Render(r.Context(), w)
		return
	}
	go func() {
		if err := s.updater.Apply(context.Background()); err != nil {
			slog.Error("manual update apply failed", "error", err)
		}
	}()
	// Return the applying state immediately
	status := s.updateStatus()
	status.Applying = true
	_ = views.UpdateBanner(status).Render(r.Context(), w)
}
