package updater

import (
	"os"
	"strings"
)

// InContainer returns true if the process is running inside a Docker, Podman,
// or other OCI container.
func InContainer() bool {
	// Docker creates this sentinel file.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// Podman creates this sentinel file.
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	// cgroup v1: path contains "docker" or "containerd".
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	content := strings.ToLower(string(data))
	return strings.Contains(content, "docker") || strings.Contains(content, "containerd")
}
