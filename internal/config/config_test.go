package config

import (
	"os"
	"path/filepath"
	"testing"
)

const validYAML = `
server:
  name: "Test Server"
  description: "A test server"
ipmi:
  host: "192.0.2.1"
  username: "admin"
wol:
  mac: "00:00:5E:00:53:01"
  broadcast: "192.0.2.255"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	t.Setenv("IPMI_PASS", "secret")
	path := writeConfig(t, validYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Name != "Test Server" {
		t.Errorf("Server.Name = %q, want %q", cfg.Server.Name, "Test Server")
	}
	if cfg.IPMI.Password != "secret" {
		t.Errorf("IPMI.Password = %q, want %q", cfg.IPMI.Password, "secret")
	}
	if cfg.IPMI.Host != "192.0.2.1" {
		t.Errorf("IPMI.Host = %q, want %q", cfg.IPMI.Host, "192.0.2.1")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("IPMI_PASS", "secret")
	path := writeConfig(t, validYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DataDir != "/var/lib/power-panel" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/var/lib/power-panel")
	}
	if cfg.Power.CooldownSec != 30 {
		t.Errorf("CooldownSec = %d, want 30", cfg.Power.CooldownSec)
	}
	if cfg.Power.BootTimeoutSec != 120 {
		t.Errorf("BootTimeoutSec = %d, want 120", cfg.Power.BootTimeoutSec)
	}
	if cfg.Power.PollIntervalSec != 5 {
		t.Errorf("PollIntervalSec = %d, want 5", cfg.Power.PollIntervalSec)
	}
	if !cfg.IPMI.Insecure {
		t.Error("IPMI.Insecure should default to true")
	}
}

func TestLoadDurationConversion(t *testing.T) {
	t.Setenv("IPMI_PASS", "secret")
	yaml := validYAML + `
power:
  cooldown_seconds: 10
  boot_timeout_seconds: 60
  poll_interval_seconds: 3
`
	path := writeConfig(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Power.Cooldown.Seconds() != 10 {
		t.Errorf("Cooldown = %v, want 10s", cfg.Power.Cooldown)
	}
	if cfg.Power.BootTimeout.Seconds() != 60 {
		t.Errorf("BootTimeout = %v, want 60s", cfg.Power.BootTimeout)
	}
	if cfg.Power.PollInterval.Seconds() != 3 {
		t.Errorf("PollInterval = %v, want 3s", cfg.Power.PollInterval)
	}
}

func TestLoadMissingIPMIPass(t *testing.T) {
	t.Setenv("IPMI_PASS", "")
	path := writeConfig(t, validYAML)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing IPMI_PASS")
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Setenv("IPMI_PASS", "secret")
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	t.Setenv("IPMI_PASS", "secret")
	path := writeConfig(t, "not: [valid: yaml")

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestValidateMissingFields(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"missing server.name", `
ipmi:
  host: "h"
  username: "u"
wol:
  mac: "00:00:5E:00:53:01"
  broadcast: "192.0.2.255"
`},
		{"missing ipmi.host", `
server:
  name: "s"
ipmi:
  username: "u"
wol:
  mac: "00:00:5E:00:53:01"
  broadcast: "192.0.2.255"
`},
		{"missing wol.mac", `
server:
  name: "s"
ipmi:
  host: "h"
  username: "u"
wol:
  broadcast: "192.0.2.255"
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("IPMI_PASS", "secret")
			path := writeConfig(t, tt.yaml)

			_, err := Load(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidatePollInterval(t *testing.T) {
	t.Setenv("IPMI_PASS", "secret")
	yaml := validYAML + `
power:
  poll_interval_seconds: 0
`
	path := writeConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for poll_interval_seconds = 0")
	}
}

func TestValidateNegativeCooldown(t *testing.T) {
	t.Setenv("IPMI_PASS", "secret")
	yaml := validYAML + `
power:
  cooldown_seconds: -1
`
	path := writeConfig(t, yaml)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for negative cooldown")
	}
}
