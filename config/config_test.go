package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var defaults = Defaults{PollInterval: 10 * time.Second, Timeout: 10 * time.Second, RequestPause: 100 * time.Millisecond}

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`
devices:
  - name: inverter
    address: 192.0.2.10:502
    profile: sungrow_hybrid
  - name: garage
    address: "[2001:db8::10]:1502"
    unit_id: 3
    profile: profiles/other.yaml
    poll_interval: 30s
    timeout: 2s
    request_pause: 250ms
    collectors: [grid, battery]
`), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(c.Devices))
	}
	first, second := c.Devices[0], c.Devices[1]
	if first.Unit() != 1 || first.PollInterval != 10*time.Second || first.Timeout != 10*time.Second || first.RequestPause != 100*time.Millisecond {
		t.Errorf("the first device did not get the defaults: %+v", first)
	}
	if second.Unit() != 3 || second.PollInterval != 30*time.Second || second.Timeout != 2*time.Second || second.RequestPause != 250*time.Millisecond {
		t.Errorf("the second device lost its settings: %+v", second)
	}
	if strings.Join(second.Collectors, ",") != "grid,battery" {
		t.Errorf("Collectors = %v", second.Collectors)
	}
}

func TestParseErrors(t *testing.T) {
	tests := map[string]string{
		"no devices":        `devices: []`,
		"unknown field":     "devices:\n  - {address: \"192.0.2.10:502\", profile: p, write: true}",
		"no port":           "devices:\n  - {address: 192.0.2.10, profile: p}",
		"no profile":        "devices:\n  - {address: \"192.0.2.10:502\"}",
		"fast poll":         "devices:\n  - {address: \"192.0.2.10:502\", profile: p, poll_interval: 1s}",
		"negative pause":    "devices:\n  - {address: \"192.0.2.10:502\", profile: p, request_pause: -1s}",
		"unit id too large": "devices:\n  - {address: \"192.0.2.10:502\", profile: p, unit_id: 300}",
	}
	for name, text := range tests {
		if _, err := Parse([]byte(text), defaults); err == nil {
			t.Errorf("%s: Parse returned no error", name)
		}
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("devices:\n  - {address: \"192.0.2.10:502\", profile: sungrow_hybrid}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, defaults); err != nil {
		t.Errorf("Load: %v", err)
	}
	if _, err := Load(path+".missing", defaults); err == nil {
		t.Error("Load of a missing file returned no error")
	}
}

// The example configuration in the repository must be valid.
func TestExampleConfig(t *testing.T) {
	c, err := Load("../examples/config.yml", defaults)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) == 0 {
		t.Error("the example has no devices")
	}
}
