package bmc

import (
	"bytes"
	"testing"
)

func TestParseMACAddress(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []byte
		wantErr bool
	}{
		{"colon separated", "00:00:5E:00:53:01", []byte{0x00, 0x00, 0x5E, 0x00, 0x53, 0x01}, false},
		{"dash separated", "00-00-5E-00-53-01", []byte{0x00, 0x00, 0x5E, 0x00, 0x53, 0x01}, false},
		{"no separator", "00005E005301", []byte{0x00, 0x00, 0x5E, 0x00, 0x53, 0x01}, false},
		{"lowercase", "00:00:5e:00:53:01", []byte{0x00, 0x00, 0x5E, 0x00, 0x53, 0x01}, false},
		{"too short", "00:00:5E", nil, true},
		{"too long", "00:00:5E:00:53:01:FF", nil, true},
		{"invalid hex", "GG:00:5E:00:53:01", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMACAddress(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMACAddress(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && !bytes.Equal(got, tt.want) {
				t.Errorf("parseMACAddress(%q) = %x, want %x", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildMagicPacket(t *testing.T) {
	mac := []byte{0x00, 0x00, 0x5E, 0x00, 0x53, 0x01}
	packet := buildMagicPacket(mac)

	if len(packet) != 102 {
		t.Fatalf("packet length = %d, want 102", len(packet))
	}

	// First 6 bytes must be 0xFF
	for i := 0; i < 6; i++ {
		if packet[i] != 0xFF {
			t.Errorf("packet[%d] = %02x, want 0xFF", i, packet[i])
		}
	}

	// 16 repetitions of the MAC address
	for i := 0; i < 16; i++ {
		offset := 6 + i*6
		got := packet[offset : offset+6]
		if !bytes.Equal(got, mac) {
			t.Errorf("MAC repetition %d = %x, want %x", i, got, mac)
		}
	}
}
