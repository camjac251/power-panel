package bmc

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"

	"github.com/camjac251/power-panel/internal/config"
)

type PowerState string

const (
	PowerOn      PowerState = "On"
	PowerOff     PowerState = "Off"
	PowerUnknown PowerState = "Unknown"

	// Transitioning states (set by server, not BMC)
	PoweringOn   PowerState = "PoweringOn"
	ShuttingDown PowerState = "ShuttingDown"
	Restarting   PowerState = "Restarting"
)

type Status struct {
	PowerState PowerState
	Health     string
	Reachable  bool // OS-level reachability (ping/TCP check)
}

type SensorData struct {
	Temperatures []Temperature
	Fans         []Fan
	PowerWatts   float64
}

type Temperature struct {
	Name    string
	Celsius float64
	Health  string
}

type Fan struct {
	Name   string
	RPM    int
	Health string
}

type Client struct {
	cfg             config.IPMIConfig
	mu              sync.Mutex
	client          *gofish.APIClient
	supportedResets []string
}

func NewClient(cfg config.IPMIConfig) *Client {
	return &Client{cfg: cfg}
}

// session returns a persistent Redfish session, reconnecting if needed.
// Caller must hold c.mu.
func (c *Client) session() (*gofish.APIClient, error) {
	if c.client != nil {
		return c.client, nil
	}

	slog.Debug("establishing Redfish session", "host", c.cfg.Host)
	client, err := gofish.Connect(gofish.ClientConfig{
		Endpoint: fmt.Sprintf("https://%s", c.cfg.Host),
		Username: c.cfg.Username,
		Password: c.cfg.Password,
		Insecure: c.cfg.Insecure,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: c.cfg.Insecure},
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
		NoModifyTransport: true,
	})
	if err != nil {
		return nil, err
	}
	c.client = client
	return client, nil
}

// disconnect drops the current session so the next call reconnects.
func (c *Client) disconnect() {
	if c.client != nil {
		c.client.Logout()
		c.client = nil
	}
}

// Close cleanly shuts down the persistent session.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnect()
}

// DiscoverCapabilities queries the BMC for supported reset types and logs them.
func (c *Client) DiscoverCapabilities() {
	c.mu.Lock()
	defer c.mu.Unlock()

	client, err := c.session()
	if err != nil {
		slog.Warn("could not discover BMC capabilities", "error", err)
		return
	}

	systems, err := client.Service.Systems()
	if err != nil || len(systems) == 0 {
		slog.Warn("could not discover BMC capabilities", "error", err)
		return
	}

	sys := systems[0]
	resetTypes, err := sys.GetSupportedResetTypes()
	if err != nil {
		slog.Warn("could not get supported reset types", "error", err)
		return // keep existing capabilities
	}
	c.supportedResets = nil
	for _, rt := range resetTypes {
		c.supportedResets = append(c.supportedResets, string(rt))
	}

	slog.Info("BMC capabilities discovered",
		"supported_resets", c.supportedResets,
		"model", sys.Model,
		"manufacturer", sys.Manufacturer,
	)
}

// SupportsReset returns true if the BMC supports a given reset type.
func (c *Client) SupportsReset(resetType string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rt := range c.supportedResets {
		if rt == resetType {
			return true
		}
	}
	return false
}

func (c *Client) GetStatus() (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	client, err := c.session()
	if err != nil {
		return Status{PowerState: PowerUnknown}, fmt.Errorf("connecting to BMC: %w", err)
	}

	systems, err := client.Service.Systems()
	if err != nil || len(systems) == 0 {
		// Session may be stale - reconnect and retry once
		c.disconnect()
		client, err = c.session()
		if err != nil {
			return Status{PowerState: PowerUnknown}, fmt.Errorf("reconnecting to BMC: %w", err)
		}
		systems, err = client.Service.Systems()
		if err != nil {
			c.disconnect()
			return Status{PowerState: PowerUnknown}, fmt.Errorf("getting systems: %w", err)
		}
		if len(systems) == 0 {
			c.disconnect()
			return Status{PowerState: PowerUnknown}, fmt.Errorf("BMC returned no systems")
		}
	}

	sys := systems[0]
	return Status{
		PowerState: PowerState(sys.PowerState),
		Health:     string(sys.Status.Health),
	}, nil
}

func (c *Client) PowerOn() error {
	return c.resetAction(schemas.OnResetType)
}

func (c *Client) PowerOff() error {
	return c.resetAction(schemas.ForceOffResetType)
}

// GracefulShutdown sends an ACPI shutdown signal to the OS.
func (c *Client) GracefulShutdown() error {
	return c.resetAction(schemas.GracefulShutdownResetType)
}

// PowerCycle performs a ForceRestart (immediate reboot).
// This BMC does not support PowerCycle; ForceRestart is the closest equivalent.
func (c *Client) PowerCycle() error {
	return c.resetAction(schemas.ForceRestartResetType)
}

func (c *Client) resetAction(resetType schemas.ResetType) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	client, err := c.session()
	if err != nil {
		return fmt.Errorf("connecting to BMC: %w", err)
	}

	systems, err := client.Service.Systems()
	if err != nil || len(systems) == 0 {
		c.disconnect()
		client, err = c.session()
		if err != nil {
			return fmt.Errorf("reconnecting to BMC: %w", err)
		}
		systems, err = client.Service.Systems()
		if err != nil {
			c.disconnect()
			return fmt.Errorf("getting systems: %w", err)
		}
		if len(systems) == 0 {
			c.disconnect()
			return fmt.Errorf("BMC returned no systems")
		}
	}

	if _, err = systems[0].Reset(resetType); err != nil {
		c.disconnect()
		return fmt.Errorf("%s: %w", resetType, err)
	}
	return nil
}

//nolint:gocognit // sensor parsing across multiple Redfish resource types
func (c *Client) GetSensors() (SensorData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	client, err := c.session()
	if err != nil {
		return SensorData{}, fmt.Errorf("connecting to BMC: %w", err)
	}

	chassis, err := client.Service.Chassis()
	if err != nil || len(chassis) == 0 {
		c.disconnect()
		client, err = c.session()
		if err != nil {
			return SensorData{}, fmt.Errorf("reconnecting to BMC: %w", err)
		}
		chassis, err = client.Service.Chassis()
		if err != nil {
			c.disconnect()
			return SensorData{}, fmt.Errorf("getting chassis: %w", err)
		}
		if len(chassis) == 0 {
			c.disconnect()
			return SensorData{}, fmt.Errorf("BMC returned no chassis")
		}
	}

	ch := chassis[0]
	data := SensorData{}

	thermal, err := ch.Thermal()
	if err != nil {
		slog.Warn("failed to read thermal data", "error", err)
	}
	if err == nil && thermal != nil {
		for i := range thermal.Temperatures {
			t := &thermal.Temperatures[i]
			var celsius float64
			if t.ReadingCelsius != nil {
				celsius = *t.ReadingCelsius
			}
			data.Temperatures = append(data.Temperatures, Temperature{
				Name:    t.Name,
				Celsius: celsius,
				Health:  string(t.Status.Health),
			})
		}
		for i := range thermal.Fans {
			f := &thermal.Fans[i]
			var rpm int
			if f.Reading != nil {
				rpm = *f.Reading
			}
			data.Fans = append(data.Fans, Fan{
				Name:   f.Name,
				RPM:    rpm,
				Health: string(f.Status.Health),
			})
		}
	}

	power, err := ch.Power()
	if err != nil {
		slog.Warn("failed to read power data", "error", err)
	}
	if err == nil && power != nil {
		if len(power.PowerControl) > 0 {
			if pc := power.PowerControl[0]; pc.PowerConsumedWatts != nil {
				data.PowerWatts = float64(*pc.PowerConsumedWatts)
			}
		}
	}

	return data, nil
}

// TimedCall wraps a BMC operation, returning the duration.
func TimedCall(fn func() error) (time.Duration, error) {
	start := time.Now()
	err := fn()
	return time.Since(start), err
}
