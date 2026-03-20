package views

import (
	"testing"
	"time"

	"github.com/camjac251/power-panel/internal/bmc"
)

func TestCleanSensorName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"CPU_PACKAGE_TEMP", "CPU Package"},
		{"CHA_FAN3", "Chassis FAN 3"},
		{"CHA_FAN1", "Chassis FAN 1"},
		{"CPU_FAN", "CPU FAN"},
		{"DIMMA1_TEMP", "DIMM A1"},
		{"PCIEO_TEMP", "PCIe"},
		{"M_2_1_TEMP", "M.2 1"},
		{"W_PUMP_TEMPERATURE", "Water Pump"},
		{"CPU_OPT_TEMP", "CPU Optional"},
		{"SYSTEM_TEMP", "System"},
		{"VRM_TEMP", "VRM"},
		{"SOC_TEMP", "SoC"},
		{"PSU_FAN", "PSU FAN"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := cleanSensorName(tt.input)
			if got != tt.want {
				t.Errorf("cleanSensorName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsKeySensor(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"CPU_PACKAGE_TEMP", true},
		{"SYSTEM_TEMP", true},
		{"CHASSIS_FAN1", true},
		{"CHA_FAN1", true},
		{"CHA_FAN2", true},
		{"CPU_FAN", true},
		{"POWER_SUPPLY", true},
		{"DIMMA1_TEMP", false},
		{"PCIEO_TEMP", false},
		{"M_2_1_TEMP", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isKeySensor(tt.name)
			if got != tt.want {
				t.Errorf("isKeySensor(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestTempSeverity(t *testing.T) {
	tests := []struct {
		celsius float64
		want    string
	}{
		{25, "normal"},
		{69.9, "normal"},
		{70, "warm"},
		{84.9, "warm"},
		{85, "hot"},
		{100, "hot"},
	}
	for _, tt := range tests {
		got := tempSeverity(tt.celsius)
		if got != tt.want {
			t.Errorf("tempSeverity(%v) = %q, want %q", tt.celsius, got, tt.want)
		}
	}
}

func TestTransitionLabel(t *testing.T) {
	tests := []struct {
		state bmc.PowerState
		want  string
	}{
		{bmc.PoweringOn, "Powering on"},
		{bmc.ShuttingDown, "Shutting down"},
		{bmc.Restarting, "Restarting"},
		{bmc.PowerOn, ""},
		{bmc.PowerOff, ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := transitionLabel(tt.state)
		if got != tt.want {
			t.Errorf("transitionLabel(%q) = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestFormatAction(t *testing.T) {
	tests := []struct {
		action string
		want   string
	}{
		{"power_on", "Power On"},
		{"power_off", "Shut Down"},
		{"power_forceoff", "Power Off"},
		{"power_cycle", "Restart"},
		{"power_reset", "Hard Reset"},
		{"some_thing", "Some thing"},
	}
	for _, tt := range tests {
		got := FormatAction(tt.action)
		if got != tt.want {
			t.Errorf("FormatAction(%q) = %q, want %q", tt.action, got, tt.want)
		}
	}
}

func TestFormatTimeAgo(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"1 min", now.Add(-90 * time.Second), "1 min ago"},
		{"5 min", now.Add(-5 * time.Minute), "5 min ago"},
		{"1 hour", now.Add(-90 * time.Minute), "1 hour ago"},
		{"3 hours", now.Add(-3 * time.Hour), "3 hours ago"},
		{"1 day", now.Add(-36 * time.Hour), "1 day ago"},
		{"5 days", now.Add(-5 * 24 * time.Hour), "5 days ago"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimeAgo(tt.t)
			if got != tt.want {
				t.Errorf("formatTimeAgo() = %q, want %q", got, tt.want)
			}
		})
	}
}
