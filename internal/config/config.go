package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig `yaml:"server"`
	IPMI    IPMIConfig   `yaml:"ipmi"`
	WoL     WoLConfig    `yaml:"wol"`
	Power   PowerConfig  `yaml:"power"`
	Update  UpdateConfig `yaml:"update"`
	DataDir string       `yaml:"data_dir"`
}

type UpdateConfig struct {
	Enabled   *bool `yaml:"enabled"`    // check for updates (default: true)
	AutoApply *bool `yaml:"auto_apply"` // apply updates automatically (default: true)
}

func (u UpdateConfig) IsEnabled() bool {
	return u.Enabled == nil || *u.Enabled
}

func (u UpdateConfig) IsAutoApply() bool {
	return u.AutoApply == nil || *u.AutoApply
}

type ServerConfig struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	NoMachineURL string `yaml:"nomachine_url"`
	PingHost     string `yaml:"ping_host"` // host or host:port to check OS reachability (default port 22)
	BMCURL       string `yaml:"bmc_url"`   // URL to BMC web UI (omit to hide button)
}

type IPMIConfig struct {
	Host     string `yaml:"host"`
	Username string `yaml:"username"`
	Password string `yaml:"-"` // from IPMI_PASS env var
	Insecure bool   `yaml:"insecure"`
}

type WoLConfig struct {
	MAC       string `yaml:"mac"`
	Broadcast string `yaml:"broadcast"`
}

type PowerConfig struct {
	Cooldown        time.Duration `yaml:"-"`
	CooldownSec     int           `yaml:"cooldown_seconds"`
	BootTimeout     time.Duration `yaml:"-"`
	BootTimeoutSec  int           `yaml:"boot_timeout_seconds"`
	PollInterval    time.Duration `yaml:"-"`
	PollIntervalSec int           `yaml:"poll_interval_seconds"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	cfg := &Config{
		DataDir: "/var/lib/power-panel",
		Power: PowerConfig{
			CooldownSec:     30,
			BootTimeoutSec:  120,
			PollIntervalSec: 5,
		},
		IPMI: IPMIConfig{
			Insecure: true,
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Load secrets from environment
	cfg.IPMI.Password = os.Getenv("IPMI_PASS")
	if cfg.IPMI.Password == "" {
		return nil, fmt.Errorf("IPMI_PASS environment variable is required")
	}

	// Validate required fields
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	// Convert seconds to durations
	cfg.Power.Cooldown = time.Duration(cfg.Power.CooldownSec) * time.Second
	cfg.Power.BootTimeout = time.Duration(cfg.Power.BootTimeoutSec) * time.Second
	cfg.Power.PollInterval = time.Duration(cfg.Power.PollIntervalSec) * time.Second

	return cfg, nil
}

func (c *Config) validate() error {
	var errs []string
	if c.Server.Name == "" {
		errs = append(errs, "server.name is required")
	}
	if c.IPMI.Host == "" {
		errs = append(errs, "ipmi.host is required")
	}
	if c.IPMI.Username == "" {
		errs = append(errs, "ipmi.username is required")
	}
	if c.WoL.MAC == "" {
		errs = append(errs, "wol.mac is required")
	}
	if c.WoL.Broadcast == "" {
		errs = append(errs, "wol.broadcast is required")
	}
	if c.Power.PollIntervalSec <= 0 {
		errs = append(errs, "power.poll_interval_seconds must be > 0")
	}
	if c.Power.CooldownSec < 0 {
		errs = append(errs, "power.cooldown_seconds must be >= 0")
	}
	if c.Power.BootTimeoutSec <= 0 {
		errs = append(errs, "power.boot_timeout_seconds must be > 0")
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation errors: %s", fmt.Sprintf("%v", errs))
	}
	return nil
}
