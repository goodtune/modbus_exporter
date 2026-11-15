package collector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/promslog"

	"github.com/goodtune/modbus_exporter/modbus"
	"github.com/goodtune/modbus_exporter/profile"
)

const testProfile = `
name: test_device
vendor: Example
address_offset: -1
word_order: low_first
no_data: {u16: 0xFFFF, s16: 0x7FFF}
model:
  register: 1
  data_type: u16
  map: {0x0010: Model Ten}
collectors:
  - {name: grid, help: Grid values.}
  - {name: battery, help: Battery values.}
blocks:
  - {name: main, start: 1, count: 12}
  - {name: battery, start: 101, count: 4}
metrics:
  - name: grid_power_watts
    help: "Power at the meter in W. Positive: import."
    collector: grid
    register: 3
    data_type: s32
  - name: grid_import_power_watts
    help: Import in W.
    collector: grid
    register: 3
    data_type: s32
    part: positive
  - name: grid_export_power_watts
    help: Export in W.
    collector: grid
    register: 3
    data_type: s32
    part: negative
  - name: grid_frequency_hertz
    help: Frequency in Hz.
    collector: grid
    register: 5
    data_type: u16
    scale: 0.01
  - name: grid_voltage_volts
    help: Voltage in V.
    collector: grid
    register: 6
    data_type: u16
    labels: {phase: a}
  - name: grid_voltage_volts
    help: Voltage in V.
    collector: grid
    register: 7
    data_type: u16
    labels: {phase: b}
  - name: grid_volt_amperes
    help: Voltage times current.
    collector: grid
    register: 6
    data_type: u16
    multiply: {register: 8, data_type: u16, scale: 0.5}
  - name: grid_state
    help: State by name.
    collector: grid
    register: 9
    data_type: u16
    value_label: state
    map: {0: Stopped, 1: Running}
  - name: grid_flow
    help: Flow bits.
    collector: grid
    register: 10
    data_type: u16
    bitmask: 0x02
    labels: {flow: importing}
  - name: grid_flow
    help: Flow bits.
    collector: grid
    register: 10
    data_type: u16
    bitmask: 0x04
    labels: {flow: exporting}
  - name: device_firmware_info
    help: Firmware version.
    collector: grid
    register: 11
    data_type: string
    count: 2
    value_label: version
    labels: {component: meter}
  - name: grid_import_energy_watt_hours_total
    help: Energy in Wh.
    type: counter
    collector: grid
    register: 1
    data_type: u16
    scale: 100
  - name: battery_state_of_charge_ratio
    help: State of charge, 0 to 1.
    collector: battery
    register: 101
    data_type: u16
    scale: 0.001
  - name: battery_temperature_celsius
    help: Temperature in °C.
    collector: battery
    register: 102
    data_type: s16
    scale: 0.1
`

// fakeReader gives fixed register values and counts the polls.
type fakeReader struct {
	mu     sync.Mutex
	words  modbus.Words
	failed map[string]error
	err    error
	polls  int
	blocks []modbus.Block
}

func (f *fakeReader) Read(_ context.Context, blocks []modbus.Block) (modbus.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	f.blocks = blocks
	if f.err != nil {
		return modbus.Result{}, f.err
	}
	result := modbus.Result{Words: modbus.Words{}, Failed: map[string]error{}}
	for _, b := range blocks {
		if err, ok := f.failed[b.Name]; ok {
			result.Failed[b.Name] = err
			continue
		}
		for i := uint16(0); i < b.Count; i++ {
			if v, ok := f.words[modbus.Key{Table: b.Table, Addr: b.Start + i}]; ok {
				result.Words[modbus.Key{Table: b.Table, Addr: b.Start + i}] = v
			}
		}
	}
	return result, nil
}

// input makes input register values from addresses on the wire.
func input(values map[uint16]uint16) modbus.Words {
	w := modbus.Words{}
	for addr, v := range values {
		w[modbus.Key{Table: modbus.Input, Addr: addr}] = v
	}
	return w
}

// The values are at the addresses on the wire: register number minus 1.
func testWords() modbus.Words {
	return input(map[uint16]uint16{
		0: 0x0010,            // model, and the energy counter: 16
		2: 0xFF9C, 3: 0xFFFF, // grid power -100, low word first
		4:  5001,   // 50.01 Hz
		5:  240,    // phase a
		6:  0xFFFF, // phase b: no data
		7:  10,     // current 10 * 0.5
		8:  1,      // state Running
		9:  0x02,   // flow: importing
		10: 'v'<<8 | '1', 11: '.'<<8 | '2',
		100: 356,    // state of charge 0.356
		101: 0xFF85, // -12.3 °C
		102: 0, 103: 0,
	})
}

func mustProfile(t *testing.T, text string) *profile.Profile {
	t.Helper()
	p, err := profile.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestCollector(t *testing.T, devices ...Device) *Collector {
	t.Helper()
	c, err := New(devices, promslog.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const scrapeNames = "modbus_scrape_collector_success"

func TestCollect(t *testing.T) {
	reader := &fakeReader{words: testWords()}
	c := newTestCollector(t, Device{
		Name: "inverter", Address: "192.0.2.10:502", Profile: mustProfile(t, testProfile),
		Reader: reader, PollInterval: time.Minute,
	})

	want := `
# HELP modbus_battery_state_of_charge_ratio State of charge, 0 to 1.
# TYPE modbus_battery_state_of_charge_ratio gauge
modbus_battery_state_of_charge_ratio{device="inverter"} 0.356
# HELP modbus_battery_temperature_celsius Temperature in °C.
# TYPE modbus_battery_temperature_celsius gauge
modbus_battery_temperature_celsius{device="inverter"} -12.3
# HELP modbus_device_firmware_info Firmware version.
# TYPE modbus_device_firmware_info gauge
modbus_device_firmware_info{component="meter",device="inverter",version="v1.2"} 1
# HELP modbus_device_info Profile, vendor and model of the device. The value is always 1.
# TYPE modbus_device_info gauge
modbus_device_info{device="inverter",model="Model Ten",profile="test_device",vendor="Example"} 1
# HELP modbus_grid_export_power_watts Export in W.
# TYPE modbus_grid_export_power_watts gauge
modbus_grid_export_power_watts{device="inverter"} 100
# HELP modbus_grid_flow Flow bits.
# TYPE modbus_grid_flow gauge
modbus_grid_flow{device="inverter",flow="exporting"} 0
modbus_grid_flow{device="inverter",flow="importing"} 1
# HELP modbus_grid_frequency_hertz Frequency in Hz.
# TYPE modbus_grid_frequency_hertz gauge
modbus_grid_frequency_hertz{device="inverter"} 50.01
# HELP modbus_grid_import_energy_watt_hours_total Energy in Wh.
# TYPE modbus_grid_import_energy_watt_hours_total counter
modbus_grid_import_energy_watt_hours_total{device="inverter"} 1600
# HELP modbus_grid_import_power_watts Import in W.
# TYPE modbus_grid_import_power_watts gauge
modbus_grid_import_power_watts{device="inverter"} 0
# HELP modbus_grid_power_watts Power at the meter in W. Positive: import.
# TYPE modbus_grid_power_watts gauge
modbus_grid_power_watts{device="inverter"} -100
# HELP modbus_grid_state State by name.
# TYPE modbus_grid_state gauge
modbus_grid_state{device="inverter",state="Running"} 1
# HELP modbus_grid_volt_amperes Voltage times current.
# TYPE modbus_grid_volt_amperes gauge
modbus_grid_volt_amperes{device="inverter"} 1200
# HELP modbus_grid_voltage_volts Voltage in V.
# TYPE modbus_grid_voltage_volts gauge
modbus_grid_voltage_volts{device="inverter",phase="a"} 240
# HELP modbus_scrape_collector_success Whether the collector got all its registers in the last poll: 1 for yes, 0 for no.
# TYPE modbus_scrape_collector_success gauge
modbus_scrape_collector_success{collector="battery",device="inverter"} 1
modbus_scrape_collector_success{collector="grid",device="inverter"} 1
`
	names := []string{
		"modbus_battery_state_of_charge_ratio", "modbus_battery_temperature_celsius", "modbus_device_firmware_info",
		"modbus_device_info", "modbus_grid_export_power_watts", "modbus_grid_flow", "modbus_grid_frequency_hertz",
		"modbus_grid_import_energy_watt_hours_total", "modbus_grid_import_power_watts", "modbus_grid_power_watts",
		"modbus_grid_state", "modbus_grid_volt_amperes", "modbus_grid_voltage_volts", scrapeNames,
	}
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), names...); err != nil {
		t.Error(err)
	}
	// The registry accepts the collector and all its metrics.
	if problems, err := testutil.CollectAndLint(c); err != nil {
		t.Errorf("lint: %v", err)
	} else {
		for _, p := range problems {
			// The names of derived test metrics do not all end in a unit.
			t.Logf("lint: %s: %s", p.Metric, p.Text)
		}
	}
}

// A scrape that comes before the poll interval is over gets the values of
// the last poll and does not go to the device.
func TestPollInterval(t *testing.T) {
	reader := &fakeReader{words: testWords()}
	c := newTestCollector(t, Device{
		Name: "inverter", Address: "192.0.2.10:502", Profile: mustProfile(t, testProfile),
		Reader: reader, PollInterval: time.Hour,
	})
	for i := 0; i < 5; i++ {
		if n := testutil.CollectAndCount(c, "modbus_grid_power_watts"); n != 1 {
			t.Fatalf("scrape %d gave %d samples of modbus_grid_power_watts", i, n)
		}
	}
	if reader.polls != 1 {
		t.Errorf("5 scrapes made %d polls, want 1", reader.polls)
	}

	// A failed poll is also not repeated before the interval is over.
	failing := &fakeReader{err: errors.New("connection refused")}
	c = newTestCollector(t, Device{
		Name: "inverter", Address: "192.0.2.10:502", Profile: mustProfile(t, testProfile),
		Reader: failing, PollInterval: time.Hour,
	})
	for i := 0; i < 3; i++ {
		testutil.CollectAndCount(c)
	}
	if failing.polls != 1 {
		t.Errorf("3 scrapes made %d failed polls, want 1", failing.polls)
	}
}

func TestPollFailed(t *testing.T) {
	reader := &fakeReader{err: errors.New("connection refused")}
	c := newTestCollector(t, Device{
		Name: "inverter", Address: "192.0.2.10:502", Profile: mustProfile(t, testProfile),
		Reader: reader, PollInterval: time.Minute,
	})
	want := `
# HELP modbus_scrape_collector_success Whether the collector got all its registers in the last poll: 1 for yes, 0 for no.
# TYPE modbus_scrape_collector_success gauge
modbus_scrape_collector_success{collector="battery",device="inverter"} 0
modbus_scrape_collector_success{collector="grid",device="inverter"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), scrapeNames); err != nil {
		t.Error(err)
	}
	// No values and no device info: only the poll time and, for each of the
	// two collectors, the duration and the success.
	if n := testutil.CollectAndCount(c); n != 5 {
		t.Errorf("a failed poll gave %d samples, want 5", n)
	}
}

// A block that the device refuses stops only the collectors that use it.
func TestBlockFailed(t *testing.T) {
	reader := &fakeReader{words: testWords(), failed: map[string]error{"battery": errors.New("illegal data address")}}
	c := newTestCollector(t, Device{
		Name: "inverter", Address: "192.0.2.10:502", Profile: mustProfile(t, testProfile),
		Reader: reader, PollInterval: time.Minute,
	})
	want := `
# HELP modbus_grid_power_watts Power at the meter in W. Positive: import.
# TYPE modbus_grid_power_watts gauge
modbus_grid_power_watts{device="inverter"} -100
# HELP modbus_scrape_collector_success Whether the collector got all its registers in the last poll: 1 for yes, 0 for no.
# TYPE modbus_scrape_collector_success gauge
modbus_scrape_collector_success{collector="battery",device="inverter"} 0
modbus_scrape_collector_success{collector="grid",device="inverter"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "modbus_grid_power_watts", scrapeNames); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(c, "modbus_battery_state_of_charge_ratio"); n != 0 {
		t.Errorf("the battery collector exported %d samples after its block failed", n)
	}
}

// Only the blocks of the enabled collectors are read.
func TestEnabledCollectors(t *testing.T) {
	reader := &fakeReader{words: testWords()}
	p := mustProfile(t, strings.Replace(testProfile, "model:\n  register: 1\n  data_type: u16\n  map: {0x0010: Model Ten}", "model: {value: Fixed Model}", 1))
	c := newTestCollector(t, Device{
		Name: "battery", Address: "192.0.2.10:502", Profile: p,
		Reader: reader, PollInterval: time.Minute, Collectors: []string{"battery"},
	})
	want := `
# HELP modbus_device_info Profile, vendor and model of the device. The value is always 1.
# TYPE modbus_device_info gauge
modbus_device_info{device="battery",model="Fixed Model",profile="test_device",vendor="Example"} 1
# HELP modbus_scrape_collector_success Whether the collector got all its registers in the last poll: 1 for yes, 0 for no.
# TYPE modbus_scrape_collector_success gauge
modbus_scrape_collector_success{collector="battery",device="battery"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "modbus_device_info", scrapeNames); err != nil {
		t.Error(err)
	}
	if len(reader.blocks) != 1 || reader.blocks[0].Name != "battery" || reader.blocks[0].Start != 100 {
		t.Errorf("the poll read the blocks %+v, want only the battery block", reader.blocks)
	}
	if n := testutil.CollectAndCount(c, "modbus_grid_power_watts"); n != 0 {
		t.Error("a disabled collector exported samples")
	}
}

// One process polls more than one device. The device label keeps the
// series apart.
func TestTwoDevices(t *testing.T) {
	first := &fakeReader{words: testWords()}
	words := testWords()
	words[modbus.Key{Table: modbus.Input, Addr: 100}] = 900
	words[modbus.Key{Table: modbus.Input, Addr: 0}] = 0x0099 // a model that the map does not have
	second := &fakeReader{words: words}
	p := mustProfile(t, testProfile)
	c := newTestCollector(t,
		Device{Name: "house", Address: "192.0.2.10:502", Profile: p, Reader: first, PollInterval: time.Minute},
		Device{Name: "shed", Address: "192.0.2.11:502", Profile: p, Reader: second, PollInterval: time.Minute},
	)
	want := `
# HELP modbus_battery_state_of_charge_ratio State of charge, 0 to 1.
# TYPE modbus_battery_state_of_charge_ratio gauge
modbus_battery_state_of_charge_ratio{device="house"} 0.356
modbus_battery_state_of_charge_ratio{device="shed"} 0.9
# HELP modbus_device_info Profile, vendor and model of the device. The value is always 1.
# TYPE modbus_device_info gauge
modbus_device_info{device="house",model="Model Ten",profile="test_device",vendor="Example"} 1
modbus_device_info{device="shed",model="unknown 0x0099",profile="test_device",vendor="Example"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "modbus_battery_state_of_charge_ratio", "modbus_device_info"); err != nil {
		t.Error(err)
	}
	// A pedantic registry checks each sample against the descriptors.
	registry := prometheus.NewPedanticRegistry()
	if err := registry.Register(c); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Gather(); err != nil {
		t.Errorf("Gather: %v", err)
	}
}

func TestNewErrors(t *testing.T) {
	p := mustProfile(t, testProfile)
	other := mustProfile(t, strings.Replace(strings.Replace(testProfile, "name: test_device", "name: other_device", 1),
		"help: Frequency in Hz.", "help: A different text.", 1))
	reader := &fakeReader{}
	device := func(name string, pr *profile.Profile, collectors ...string) Device {
		return Device{Name: name, Address: "192.0.2.10:502", Profile: pr, Reader: reader, PollInterval: time.Minute, Collectors: collectors}
	}
	tests := map[string][]Device{
		"no devices":         nil,
		"no name":            {device("", p)},
		"name two times":     {device("a", p), device("a", p)},
		"no profile":         {device("a", nil)},
		"unknown collector":  {device("a", p, "grid", "weather")},
		"help text conflict": {device("a", p), device("b", other)},
	}
	for name, devices := range tests {
		if _, err := New(devices, promslog.NewNopLogger()); err == nil {
			t.Errorf("%s: New returned no error", name)
		}
	}
	if _, err := New([]Device{{Name: "a", Profile: p, PollInterval: time.Minute}}, promslog.NewNopLogger()); err == nil {
		t.Error("New accepted a device without a reader")
	}
}

// The embedded profile gives the values of the reference poll below. The
// register values are invented, but they have the structure of a real poll.
func TestSungrowHybridProfile(t *testing.T) {
	p, err := profile.Load("sungrow_hybrid", "")
	if err != nil {
		t.Fatal(err)
	}
	words := modbus.Words{}
	for _, b := range p.Blocks {
		w := b.Wire()
		for i := uint16(0); i < w.Count; i++ {
			words[modbus.Key{Table: w.Table, Addr: w.Start + i}] = 0
		}
	}
	for addr, v := range map[uint16]uint16{
		4999: 0x0E25, 5000: 150, 5007: 415, // model, 15 kW, 41.5 °C
		5010: 4000, 5011: 25, // MPPT 1: 400 V, 2.5 A
		5016: 1000, 5017: 0, // PV 1000 W
		5018: 2400, 5241: 4998,
		5600: 0xFF38, 5601: 0xFFFF, // meter -200 W: export
		5213: 0xFE0C, 5214: 0xFFFF, // battery -500 W: charge
		5638:  1280,   // 12.8 kWh
		12999: 0x0000, // Running
		13000: 0x01 | 0x02 | 0x08 | 0x10,
		13007: 300, 13008: 0, // load
		13022: 505,                   // state of charge 50.5 %
		13045: 0x86A0, 13046: 0x0001, // 100000 * 100 Wh
		13249: 'A'<<8 | 'B', 13250: 'C' << 8,
	} {
		words[modbus.Key{Table: modbus.Input, Addr: addr}] = v
	}
	c := newTestCollector(t, Device{
		Name: "inverter", Address: "192.0.2.10:502", Profile: p,
		Reader: &fakeReader{words: words}, PollInterval: time.Minute,
	})

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(c)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, family := range families {
		if strings.Contains(family.GetName(), "sungrow") {
			t.Errorf("metric name %s has the vendor in it", family.GetName())
		}
		for _, m := range family.GetMetric() {
			id := family.GetName()
			for _, l := range m.GetLabel() {
				if l.GetName() != "device" {
					id += "/" + l.GetValue()
				} else if l.GetValue() != "inverter" {
					t.Errorf("%s has the device label %q", id, l.GetValue())
				}
			}
			if m.GetCounter() != nil {
				got[id] = m.GetCounter().GetValue()
			} else {
				got[id] = m.GetGauge().GetValue()
			}
		}
	}
	want := map[string]float64{
		"modbus_device_info/SH15T/sungrow_hybrid/Sungrow": 1,
		"modbus_device_firmware_info/inverter/ABC":        1,
		"modbus_inverter_rated_power_watts":               15000,
		"modbus_inverter_temperature_celsius":             41.5,
		"modbus_inverter_running_state/Running":           1,
		"modbus_inverter_power_flow/pv_generating":        1,
		"modbus_inverter_power_flow/battery_discharging":  0,
		"modbus_inverter_power_flow/grid_exporting":       1,
		"modbus_pv_power_watts":                           1000,
		"modbus_pv_mppt_voltage_volts/1":                  400,
		"modbus_pv_mppt_power_watts/1":                    1000,
		"modbus_grid_voltage_volts/a":                     240,
		"modbus_grid_frequency_hertz":                     49.98,
		"modbus_grid_power_watts":                         -200,
		"modbus_grid_import_power_watts":                  0,
		"modbus_grid_export_power_watts":                  200,
		"modbus_battery_power_watts":                      -500,
		"modbus_battery_charge_power_watts":               500,
		"modbus_battery_discharge_power_watts":            0,
		"modbus_battery_capacity_watt_hours":              12800,
		"modbus_battery_state_of_charge_ratio":            0.505,
		"modbus_load_power_watts":                         300,
		"modbus_grid_export_energy_watt_hours_total":      10000000,
		"modbus_scrape_collector_success/energy":          1,
		"modbus_scrape_collector_success/inverter":        1,
	}
	for id, value := range want {
		v, ok := got[id]
		if !ok {
			t.Errorf("no sample %s", id)
		} else if diff := v - value; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s = %v, want %v", id, v, value)
		}
	}
}
