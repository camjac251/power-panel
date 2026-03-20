package bmc

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"github.com/camjac251/power-panel/internal/config"
)

type WoLClient struct {
	cfg config.WoLConfig
}

func NewWoLClient(cfg config.WoLConfig) *WoLClient {
	return &WoLClient{cfg: cfg}
}

// Wake sends a Wake-on-LAN magic packet.
func (w *WoLClient) Wake(ctx context.Context) error {
	mac, err := parseMACAddress(w.cfg.MAC)
	if err != nil {
		return fmt.Errorf("parsing MAC: %w", err)
	}

	packet := buildMagicPacket(mac)

	addr := w.cfg.Broadcast + ":9"
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", addr)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", addr, err)
	}
	defer conn.Close()

	if _, err := conn.Write(packet); err != nil {
		return fmt.Errorf("sending magic packet: %w", err)
	}

	return nil
}

func parseMACAddress(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 12 {
		return nil, fmt.Errorf("invalid MAC address length: %s", s)
	}
	return hex.DecodeString(s)
}

func buildMagicPacket(mac []byte) []byte {
	// 6 bytes of 0xFF followed by 16 repetitions of the MAC address
	packet := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}
	return packet
}
