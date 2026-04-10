package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/camjac251/power-panel/internal/bmc"
	"github.com/camjac251/power-panel/internal/db"
	"github.com/camjac251/power-panel/views"
)

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ticker := time.NewTicker(s.cfg.Power.PollInterval)
	defer ticker.Stop()

	var prevState bmc.PowerState
	prevState, err := s.sendStatusEventWithTransition(w, flusher, r, prevState)
	if err != nil {
		return
	}

	for {
		notifyCh := s.getNotifyCh()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			prevState, err = s.sendStatusEventWithTransition(w, flusher, r, prevState)
			if err != nil {
				return
			}
		case <-notifyCh:
			prevState, err = s.sendStatusEventWithTransition(w, flusher, r, prevState)
			if err != nil {
				return
			}
		}
	}
}

//nolint:gocognit,cyclop,funlen // SSE event rendering with transition state machine
func (s *Server) sendStatusEventWithTransition(w http.ResponseWriter, flusher http.Flusher, r *http.Request, prevState bmc.PowerState) (bmc.PowerState, error) {
	// Use cached BMC result so multiple SSE clients share one BMC call per interval
	cached := s.bmcCache.Get()
	status := cached.Status
	err := cached.Err

	// Check OS reachability if configured and machine is on
	if status.PowerState == bmc.PowerOn {
		if s.cfg.Server.PingHost != "" {
			status.Reachable = bmc.CheckReachable(r.Context(), s.cfg.Server.PingHost, 2*time.Second)
		} else {
			status.Reachable = true // No ping configured, assume reachable
		}
	}

	realState := status.PowerState

	// Apply transition override if active
	if tc := s.getTransition(); tc.State != "" { //nolint:nestif // transition state machine is inherently branchy
		confirmed := false
		switch tc.State {
		case bmc.PoweringOn:
			confirmed = realState == bmc.PowerOn
		case bmc.ShuttingDown:
			confirmed = realState == bmc.PowerOff
			// NCSI shared NIC drops when host powers down, causing Redfish errors.
			// Treat BMC-unreachable during shutdown as confirmation the host is off.
			if err != nil {
				slog.Info("BMC unreachable during shutdown, treating as powered off")
				confirmed = true
				status.PowerState = bmc.PowerOff
			}
		case bmc.Restarting:
			confirmed = realState == bmc.PowerOn && prevState == bmc.PowerOff
			// If the reboot was faster than the poll interval, we never saw PowerOff.
			// Confirm if the machine is on and enough time has passed for the restart.
			if !confirmed && realState == bmc.PowerOn && tc.Age > 15*time.Second {
				confirmed = true
			}
			if err != nil {
				slog.Info("BMC unreachable during restart, treating as powered off")
				status.PowerState = bmc.PowerOff
			}
		}
		// Check the BMC task for late-arriving failures. Some firmware accepts
		// the IPMI command but later marks the task as Exception. Without this
		// check the UI would show the transition state until the boot timeout.
		taskFailed := false
		if !confirmed && tc.TaskURL != "" {
			info, taskErr := s.bmc.GetTaskInfo(tc.TaskURL)
			if taskErr != nil {
				slog.Debug("could not poll task state", "task_url", tc.TaskURL, "error", taskErr)
			} else if info.State == "Exception" || info.State == "Killed" {
				slog.Warn("BMC task reported failure",
					"action", tc.Action,
					"task_url", tc.TaskURL,
					"state", info.State,
					"status", info.Status,
					"message", info.Message,
				)
				ev := db.PowerEvent{
					Action:    tc.Action,
					UserLogin: tc.User,
					Success:   false,
					ErrorMsg:  fmt.Sprintf("BMC task %s: %s", info.State, info.Message),
				}
				if logErr := s.store.LogEvent(r.Context(), ev); logErr != nil {
					slog.Error("failed to log task failure event", "error", logErr)
				}
				taskFailed = true
			}
		}
		slog.Info("SSE transition check", "transition", tc.State, "realState", realState, "prevState", prevState, "confirmed", confirmed, "task_failed", taskFailed, "bmcErr", err != nil)
		if confirmed || taskFailed {
			s.clearTransition()
		} else {
			status.PowerState = tc.State
		}
	}

	ctx := r.Context()
	igniting := prevState != "" && prevState != bmc.PowerOn && status.PowerState == bmc.PowerOn

	// Send the status badge
	var buf bytes.Buffer
	if renderErr := views.StatusBadge(status).Render(ctx, &buf); renderErr != nil {
		slog.Error("SSE render badge failed", "error", renderErr)
		return status.PowerState, renderErr
	}
	if err := writeSSEEvent(w, "status", buf.Bytes()); err != nil {
		return status.PowerState, err
	}

	// Send the power button (with ignite animation on Off->On transition)
	buf.Reset()
	if igniting {
		if renderErr := views.PowerButtonIgnite().Render(ctx, &buf); renderErr != nil {
			slog.Error("SSE render power ignite failed", "error", renderErr)
			return status.PowerState, renderErr
		}
	} else {
		if renderErr := views.PowerButton(status).Render(ctx, &buf); renderErr != nil {
			slog.Error("SSE render power button failed", "error", renderErr)
			return status.PowerState, renderErr
		}
	}
	if err := writeSSEEvent(w, "power", buf.Bytes()); err != nil {
		return status.PowerState, err
	}

	// Send the action buttons
	buf.Reset()
	if renderErr := views.ActionButtons(status, s.cfg.Server.NoMachineURL, s.capabilities()).Render(ctx, &buf); renderErr != nil {
		slog.Error("SSE render action buttons failed", "error", renderErr)
		return status.PowerState, renderErr
	}
	if err := writeSSEEvent(w, "actions", buf.Bytes()); err != nil {
		return status.PowerState, err
	}

	// Send sensor data (skip if BMC was unreachable, SSE stream continues)
	if cached.Err != nil {
		flusher.Flush()
		return status.PowerState, nil //nolint:nilerr // intentional: BMC errors skip sensors but don't kill the SSE stream
	}
	sensors := cached.Sensors
	buf.Reset()
	if renderErr := views.KeySensors(sensors).Render(ctx, &buf); renderErr != nil {
		slog.Error("SSE render key sensors failed", "error", renderErr)
	} else if err := writeSSEEvent(w, "sensors-key", buf.Bytes()); err != nil {
		return status.PowerState, err
	}

	buf.Reset()
	if renderErr := views.AllSensors(sensors).Render(ctx, &buf); renderErr != nil {
		slog.Error("SSE render all sensors failed", "error", renderErr)
	} else if err := writeSSEEvent(w, "sensors-all", buf.Bytes()); err != nil {
		return status.PowerState, err
	}

	flusher.Flush()
	return status.PowerState, nil
}

func writeSSEEvent(w http.ResponseWriter, event string, data []byte) error {
	lines := bytes.Split(data, []byte("\n"))
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	for _, line := range lines {
		if _, err := w.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return err
		}
	}
	_, err := w.Write([]byte("\n"))
	return err
}
