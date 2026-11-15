package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goodtune/modbus_exporter/modbus"
)

const minimal = `
name: test_device
vendor: Example
address_offset: -1
word_order: low_first
no_data: {u16: 0xFFFF}
model:
  register: 1
  data_type: u16
  map: {0x0010: Model Ten}
collectors:
  - {name: main, help: All values.}
blocks:
  - {name: first, start: 1, count: 10}
  - {name: settings, table: holding, start: 101, count: 4}
metrics:
  - name: grid_power_watts
    help: Power in W.
    collector: main
    register: 3
    data_type: s32
  - name: voltage_volts
    help: Voltage in V.
    collector: main
    register: 5
    data_type: u16
    scale: 0.1
    labels: {phase: a}
  - name: voltage_volts
    help: Voltage in V.
    collector: main
    register: 6
    data_type: u16
    scale: 0.1
    labels: {phase: b}
  - name: limit_watts
    help: Limit in W.
    collector: main
    table: holding
    register: 101
    data_type: u16
`

func TestParse(t *testing.T) {
	p, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if p.Order() != modbus.LowWordFirst {
		t.Error("the word order is not low_first")
	}
	if !p.IsNoData(modbus.U16, 0xFFFF) || p.IsNoData(modbus.U16, 1) || p.IsNoData(modbus.S16, 0xFFFF) {
		t.Error("IsNoData is not correct")
	}
	if got := p.Blocks[0].Wire(); got != (modbus.Block{Name: "first", Table: modbus.Input, Start: 0, Count: 10}) {
		t.Errorf("first block on the wire = %+v", got)
	}
	if got := p.Blocks[1].Wire(); got.Table != modbus.Holding || got.Start != 100 {
		t.Errorf("second block on the wire = %+v", got)
	}

	power := p.Metrics[0]
	if power.Addr() != 2 || power.Kind() != modbus.S32 || power.BlockName() != "first" || power.Type != "gauge" || power.Scale != 1 {
		t.Errorf("grid_power_watts: addr %d, kind %v, block %q, type %q, scale %v",
			power.Addr(), power.Kind(), power.BlockName(), power.Type, power.Scale)
	}
	if got := p.Metrics[1].LabelNames(); len(got) != 1 || got[0] != "phase" {
		t.Errorf("LabelNames = %v", got)
	}
	limit := p.Metrics[3]
	if limit.WireTable() != modbus.Holding || limit.Addr() != 100 || limit.BlockName() != "settings" {
		t.Errorf("limit_watts: table %v, addr %d, block %q", limit.WireTable(), limit.Addr(), limit.BlockName())
	}
	if p.Model.Addr() != 0 || p.Model.Map[0x10] != "Model Ten" {
		t.Errorf("model: addr %d, map %v", p.Model.Addr(), p.Model.Map)
	}
}

// replace changes one line of the minimal profile.
func replace(old, new string) string {
	if !strings.Contains(minimal, old) {
		panic("the minimal profile does not contain " + old)
	}
	return strings.Replace(minimal, old, new, 1)
}

func TestParseErrors(t *testing.T) {
	tests := map[string]string{
		"empty":                    ``,
		"unknown field":            replace("vendor: Example", "vendor: Example\nwrite_registers: []"),
		"unknown metric field":     replace("    data_type: s32", "    data_type: s32\n    function: 6"),
		"bad profile name":         replace("name: test_device", "name: Test Device"),
		"bad word order":           replace("word_order: low_first", "word_order: middle"),
		"bad no_data type":         replace("no_data: {u16: 0xFFFF}", "no_data: {f32: 1}"),
		"no_data too large":        replace("no_data: {u16: 0xFFFF}", "no_data: {u16: 0x10000}"),
		"write table":              replace("table: holding, start: 101", "table: coil, start: 101"),
		"metric write table":       replace("    table: holding\n", "    table: write\n"),
		"block too large":          replace("start: 1, count: 10", "start: 1, count: 126"),
		"block before 0":           replace("start: 1, count: 10", "start: 0, count: 10"),
		"blocks overlap":           replace("table: holding, start: 101", "start: 10"),
		"block defined two times":  replace("name: settings", "name: first"),
		"register not in a block":  replace("register: 5\n", "register: 50\n"),
		"value over the block end": replace("register: 3\n", "register: 10\n"),
		"register in other table":  replace("    table: holding\n", ""),
		"bad data type":            replace("data_type: s32", "data_type: s64"),
		"bad metric name":          replace("name: grid_power_watts", "name: Grid-Power"),
		"reserved metric name":     replace("name: grid_power_watts", "name: scrape_collector_success"),
		"reserved info name":       replace("name: grid_power_watts", "name: device_info"),
		"no help":                  replace("help: Power in W.", "help: \"\""),
		"bad type":                 replace("help: Power in W.", "help: Power in W.\n    type: histogram"),
		"unknown collector":        replace("collector: main\n    register: 3", "collector: other\n    register: 3"),
		"device label":             replace("labels: {phase: a}", "labels: {device: a}"),
		"bad label name":           replace("labels: {phase: a}", "labels: {Phase-1: a}"),
		"same labels two times":    replace("labels: {phase: b}", "labels: {phase: a}"),
		"different label names":    replace("labels: {phase: b}", "labels: {line: b}"),
		"different help":           replace("help: Voltage in V.\n    collector: main\n    register: 6", "help: Other.\n    collector: main\n    register: 6"),
		"bad part":                 replace("data_type: s32", "data_type: s32\n    part: middle"),
		"map without value_label":  replace("data_type: s32", "data_type: s32\n    map: {1: one}"),
		"value_label without map":  replace("data_type: s32", "data_type: s32\n    value_label: state"),
		"value_label with scale":   replace("data_type: s32", "data_type: s32\n    value_label: state\n    map: {1: one}\n    scale: 2"),
		"string without label":     replace("data_type: s32", "data_type: string\n    count: 2"),
		"string without count":     replace("data_type: s32", "data_type: string\n    value_label: text"),
		"count without string":     replace("data_type: s32", "data_type: s32\n    count: 2"),
		"bitmask with scale":       replace("data_type: s32", "data_type: s32\n    bitmask: 1\n    scale: 2"),
		"bitmask counter":          replace("data_type: s32", "data_type: s32\n    bitmask: 1\n    type: counter"),
		"multiply not in a block":  replace("data_type: s32", "data_type: s32\n    multiply: {register: 70, data_type: u16}"),
		"model value and register": replace("model:\n", "model:\n  value: Fixed\n"),
		"model without map":        replace("  map: {0x0010: Model Ten}\n", ""),
		"no collectors":            replace("  - {name: main, help: All values.}", "  []"),
	}
	for name, text := range tests {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s: Parse returned no error", name)
		}
	}
}

func TestDerivedMetrics(t *testing.T) {
	text := minimal + `
  - name: state
    help: State by name.
    collector: main
    register: 7
    data_type: u16
    value_label: state
    map: {0: Stopped, 1: Running}
  - name: firmware_info
    help: Firmware.
    collector: main
    register: 8
    data_type: string
    count: 3
    value_label: version
    labels: {component: main}
  - name: flow
    help: Flow bits.
    collector: main
    register: 7
    data_type: u16
    bitmask: 0x04
    labels: {flow: charging}
  - name: import_power_watts
    help: Import in W.
    collector: main
    register: 3
    data_type: s32
    part: positive
  - name: string_power_watts
    help: Volts times amperes.
    collector: main
    register: 5
    data_type: u16
    scale: 0.1
    multiply: {register: 6, data_type: u16, scale: 0.1}
`
	p, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	firmware := p.Metrics[5]
	if !firmware.IsText() || strings.Join(firmware.LabelNames(), ",") != "component,version" {
		t.Errorf("firmware_info: text %v, labels %v", firmware.IsText(), firmware.LabelNames())
	}
	if m := p.Metrics[8].Multiply; m == nil || m.Addr() != 5 || m.Scale != 0.1 {
		t.Errorf("multiply = %+v", m)
	}
}

func TestEmbedded(t *testing.T) {
	names := Embedded()
	if len(names) == 0 {
		t.Fatal("no embedded profile")
	}
	for _, name := range names {
		p, err := Load(name, "")
		if err != nil {
			t.Errorf("embedded profile %s: %v", name, err)
			continue
		}
		if p.Name != name {
			t.Errorf("profile in %s.yaml has the name %q", name, p.Name)
		}
		if p.Vendor == "" || p.Model == nil {
			t.Errorf("profile %s has no vendor or no model", name)
		}
	}
}

// The addresses that were verified on a live device must stay the same.
func TestSungrowHybrid(t *testing.T) {
	p, err := Load("sungrow_hybrid", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Order() != modbus.LowWordFirst || p.AddressOffset != -1 {
		t.Errorf("word order %v, address offset %d", p.Order(), p.AddressOffset)
	}
	var requests, words int
	for _, b := range p.Blocks {
		requests++
		words += int(b.Wire().Count)
		if b.Wire().Table != modbus.Input {
			t.Errorf("block %s is not an input register block", b.Name)
		}
	}
	if requests != 7 || words != 154 {
		t.Errorf("a poll is %d requests and %d registers, want 7 and 154", requests, words)
	}
	// Addresses on the wire: the register number of the document minus 1.
	want := map[string]uint16{
		"grid_power_watts":                    5600,
		"grid_frequency_hertz":                5241,
		"pv_power_watts":                      5016,
		"load_power_watts":                    13007,
		"battery_power_watts":                 5213,
		"battery_state_of_charge_ratio":       13022,
		"inverter_active_power_watts":         13033,
		"grid_export_energy_watt_hours_total": 13045,
	}
	for _, m := range p.Metrics {
		if addr, ok := want[m.Name]; ok {
			if m.Addr() != addr {
				t.Errorf("%s is at address %d on the wire, want %d", m.Name, m.Addr(), addr)
			}
			delete(want, m.Name)
		}
		if strings.Contains(m.Name, "sungrow") {
			t.Errorf("metric name %s has the vendor in it", m.Name)
		}
	}
	for name := range want {
		t.Errorf("the profile has no metric %s", name)
	}
	if p.Model.Addr() != 4999 || p.Model.Map[0x0E25] != "SH15T" {
		t.Errorf("model: address %d, 0x0E25 = %q", p.Model.Addr(), p.Model.Map[0x0E25])
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "device.yaml"), []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	// A relative path starts at the base directory.
	if p, err := Load("device.yaml", dir); err != nil || p.Name != "test_device" {
		t.Errorf("Load(relative) = %v, %v", p, err)
	}
	if _, err := Load(filepath.Join(dir, "device.yaml"), "/nonexistent"); err != nil {
		t.Errorf("Load(absolute): %v", err)
	}
	_, err := Load("no_such_profile", dir)
	if err == nil || !strings.Contains(err.Error(), "sungrow_hybrid") {
		t.Errorf("Load(unknown) = %v, want an error that lists the embedded profiles", err)
	}
	if _, err := Load("", dir); err == nil {
		t.Error("Load without a name returned no error")
	}
}

// The example profile in the README must be a valid profile.
func TestReadmeExample(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(readme), "```yaml\nname: example_meter")
	if !found {
		t.Fatal("the README has no example profile")
	}
	body, _, _ := strings.Cut(rest, "```")
	p, err := Parse([]byte("name: example_meter" + body))
	if err != nil {
		t.Fatalf("the example profile of the README is not valid: %v", err)
	}
	if len(p.Metrics) != 2 || p.Metrics[0].Addr() != 10 {
		t.Errorf("the example profile has %d metrics, the first at address %d", len(p.Metrics), p.Metrics[0].Addr())
	}
}
