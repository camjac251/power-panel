package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/camjac251/power-panel/internal/bmc"
)

func FormatAction(action string) string {
	switch action {
	case "power_on":
		return "Power On"
	case "power_off":
		return "Shut Down"
	case "power_forceoff":
		return "Power Off"
	case "power_cycle":
		return "Restart"
	case "power_reset":
		return "Hard Reset"
	default:
		s := strings.ReplaceAll(action, "_", " ")
		if s != "" {
			return strings.ToUpper(s[:1]) + s[1:]
		}
		return s
	}
}

var sensorNameReplacer = strings.NewReplacer(
	"CHA ", "Chassis ",
	"cha ", "Chassis ",
	"DIMM", "DIMM ",
	"PCIEO", "PCIe ",
	"pcieo", "PCIe ",
	"PCIE", "PCIe ",
	"PSU", "PSU ",
	"W PUMP", "Water Pump",
	"w pump", "Water Pump",
	"CPU OPT", "CPU Optional",
	"cpu opt", "CPU Optional",
	"M 2 ", "M.2 ",
	"m 2 ", "M.2 ",
	"SOC", "SoC",
)

// cleanSensorName makes raw BMC sensor names readable.
// "CPU_PACKAGE_TEMP" -> "CPU Package", "CHA_FAN3" -> "Chassis Fan 3"
//
//nolint:gocognit // sensor name normalization has many transformation steps
func cleanSensorName(name string) string {
	upper := strings.ToUpper(name)

	// Remove common suffixes (case-insensitive)
	for _, suffix := range []string{"_TEMP", "_TEMPERATURE", " TEMP", " TEMPERATURE"} {
		if strings.HasSuffix(upper, suffix) {
			name = name[:len(name)-len(suffix)]
			break
		}
	}

	// Replace underscores and clean up
	name = strings.ReplaceAll(name, "_", " ")
	name = sensorNameReplacer.Replace(name)

	// Separate trailing numbers from long words: "FAN3" -> "Fan 3" but keep "G1" as-is
	words := strings.Fields(name)
	for i, w := range words {
		if len(w) > 2 {
			runes := []rune(w)
			var buf []rune
			for j, r := range runes {
				if j > 0 && r >= '0' && r <= '9' {
					prev := runes[j-1]
					if (prev < '0' || prev > '9') && prev != '.' {
						buf = append(buf, ' ')
					}
				}
				buf = append(buf, r)
			}
			words[i] = string(buf)
		}
	}
	name = strings.Join(words, " ")

	// Title case each word
	words = strings.Fields(name)
	for i, w := range words {
		upper := strings.ToUpper(w)
		lower := strings.ToLower(w)
		// Keep short all-caps acronyms (CPU, FAN, VRM), numbers,
		// and mixed-case words already formatted by the replacer (PCIe, SoC)
		if len(w) <= 4 && w == upper {
			continue
		}
		if w[0] >= '0' && w[0] <= '9' {
			continue
		}
		if w != upper && w != lower {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}

// isKeySensor returns true for important sensors that should always be visible.
func isKeySensor(name string) bool {
	upper := strings.ToUpper(name)
	for _, key := range []string{"CPU", "PACKAGE", "POWER", "SYSTEM", "CHASSIS", "CHA_FAN1", "CHA_FAN2", "CPU_FAN"} {
		if strings.Contains(upper, key) {
			return true
		}
	}
	return false
}

// tempSeverity returns "normal", "warm", or "hot" based on temperature thresholds.
func tempSeverity(celsius float64) string {
	switch {
	case celsius >= 85:
		return "hot"
	case celsius >= 70:
		return "warm"
	default:
		return "normal"
	}
}

func transitionLabel(state bmc.PowerState) string {
	switch state {
	case bmc.PoweringOn:
		return "Powering on"
	case bmc.ShuttingDown:
		return "Shutting down"
	case bmc.Restarting:
		return "Restarting"
	default:
		return ""
	}
}

func formatTimeAgo(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		return "just now"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d min ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
