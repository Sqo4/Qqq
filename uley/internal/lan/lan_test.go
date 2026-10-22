package lan

import (
	"net"
	"testing"
)

func TestIsHome(t *testing.T) {
	for s, want := range map[string]bool{
		"192.168.1.5": true, "10.147.17.3": true, "26.12.3.4": true,
		"100.101.2.3": true, "8.8.8.8": false, "100.128.0.1": false,
	} {
		if got := IsHome(net.ParseIP(s)); got != want {
			t.Errorf("%s: %v", s, got)
		}
	}
}
