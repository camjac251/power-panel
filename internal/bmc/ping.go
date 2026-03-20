package bmc

import (
	"context"
	"net"
	"time"
)

// CheckReachable tests if a host is reachable by attempting a TCP connection.
// Accepts "host", "host:port", or "[ipv6]:port". Defaults to port 22 if omitted.
func CheckReachable(ctx context.Context, host string, timeout time.Duration) bool {
	if host == "" {
		return false
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "22")
	}
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", host)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
