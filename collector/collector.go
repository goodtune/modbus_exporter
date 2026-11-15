// Package collector turns the register values of Modbus devices into
// Prometheus metrics. A device profile says which registers to read and
// which metric each register makes. The package has no knowledge of a
// vendor or a type of device.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/goodtune/modbus_exporter/modbus"
	"github.com/goodtune/modbus_exporter/profile"
)

const namespace = "modbus"

// Reader reads blocks of registers from one device. The exporter has no
// other access to a device.
type Reader interface {
	Read(ctx context.Context, blocks []modbus.Block) (modbus.Result, error)
}

// Device is one device that the collector polls.
type Device struct {
	// Name is the value of the device label.
	Name string
	// Address identifies the connection. Devices with the same address are
	// polled one after the other, because many devices and gateways accept
	// only one client at a time.
	Address string
	Profile *profile.Profile
	Reader  Reader
	// PollInterval is the shortest time between two polls of the device. A
	// scrape that comes sooner gets the values of the last poll.
	PollInterval time.Duration
	// Collectors lists the collectors of the profile to enable. An empty
	// list enables all.
	Collectors []string
}

// poll is the outcome of one poll of a device.
type poll struct {
	at       time.Time
	duration time.Duration
	result   modbus.Result
	err      error
}

// metric is one metric of a profile, with its descriptor.
type metric struct {
	spec      profile.Metric
	desc      *prometheus.Desc
	valueType prometheus.ValueType
	// labels has the device name and the values of the fixed labels.
	labels []string
}

// group is one enabled collector of a device.
type group struct {
	name    string
	blocks  []string
	metrics []metric
}

type device struct {
	Device
	blocks []modbus.Block
	groups []group

	mu   sync.Mutex
	last *poll
}

// Collector polls the devices and makes their metrics.
type Collector struct {
	devices []*device
	// byAddress has the devices of each address, in the sequence of the
	// configuration.
	byAddress [][]*device
	descs     []*prometheus.Desc
	logger    *slog.Logger
}

var (
	scrapeDurationDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_duration_seconds"),
		"Time the last poll of the device took. All collectors of a device use the same poll.",
		[]string{profile.DeviceLabel, "collector"}, nil,
	)
	scrapeSuccessDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_success"),
		"Whether the collector got all its registers in the last poll: 1 for yes, 0 for no.",
		[]string{profile.DeviceLabel, "collector"}, nil,
	)
	pollTimeDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "poll_timestamp_seconds"),
		"Time of the last poll of the device, in seconds since the Unix epoch.",
		[]string{profile.DeviceLabel}, nil,
	)
	deviceInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "device", "info"),
		"Profile, vendor and model of the device. The value is always 1.",
		[]string{profile.DeviceLabel, "profile", "vendor", "model"}, nil,
	)
)

// family is what all metrics with the same name must have in common.
type family struct {
	desc      *prometheus.Desc
	help      string
	labels    string
	valueType prometheus.ValueType
}

// New makes the collector for the devices.
func New(devices []Device, logger *slog.Logger) (*Collector, error) {
	if len(devices) == 0 {
		return nil, errors.New("no device is given")
	}
	c := &Collector{
		descs:  []*prometheus.Desc{scrapeDurationDesc, scrapeSuccessDesc, pollTimeDesc, deviceInfoDesc},
		logger: logger,
	}
	names := map[string]bool{}
	addresses := map[string]int{}
	families := map[string]family{}
	for _, d := range devices {
		if d.Name == "" {
			return nil, errors.New("a device has no name")
		}
		if names[d.Name] {
			return nil, fmt.Errorf("device name %q is used two times", d.Name)
		}
		names[d.Name] = true
		dev, err := c.newDevice(d, families)
		if err != nil {
			return nil, fmt.Errorf("device %q: %w", d.Name, err)
		}
		c.devices = append(c.devices, dev)
		i, ok := addresses[d.Address]
		if !ok {
			i = len(c.byAddress)
			addresses[d.Address] = i
			c.byAddress = append(c.byAddress, nil)
		}
		c.byAddress[i] = append(c.byAddress[i], dev)
	}
	for _, f := range families {
		c.descs = append(c.descs, f.desc)
	}
	return c, nil
}

func (c *Collector) newDevice(d Device, families map[string]family) (*device, error) {
	p := d.Profile
	if p == nil || d.Reader == nil {
		return nil, errors.New("the device needs a profile and a reader")
	}
	enabled := map[string]bool{}
	for _, name := range d.Collectors {
		enabled[name] = true
	}
	needed := map[string]bool{}
	dev := &device{Device: d}
	for _, pc := range p.Collectors {
		if len(d.Collectors) > 0 && !enabled[pc.Name] {
			continue
		}
		delete(enabled, pc.Name)
		g := group{name: pc.Name}
		blocks := map[string]bool{}
		for _, spec := range p.Metrics {
			if spec.Collector != pc.Name {
				continue
			}
			m, err := newMetric(d.Name, spec, families)
			if err != nil {
				return nil, err
			}
			g.metrics = append(g.metrics, m)
			blocks[spec.BlockName()] = true
			if spec.Multiply != nil {
				blocks[spec.Multiply.BlockName()] = true
			}
		}
		for name := range blocks {
			g.blocks = append(g.blocks, name)
			needed[name] = true
		}
		dev.groups = append(dev.groups, g)
	}
	for name := range enabled {
		return nil, fmt.Errorf("the profile %s has no collector %q", p.Name, name)
	}
	if len(dev.groups) == 0 {
		return nil, errors.New("no collector is enabled")
	}
	if p.Model != nil && p.Model.Value == "" {
		needed[p.Model.BlockName()] = true
	}
	// The blocks are read in the sequence of the profile.
	for _, b := range p.Blocks {
		if needed[b.Name] {
			dev.blocks = append(dev.blocks, b.Wire())
		}
	}
	return dev, nil
}

// newMetric makes the metric of spec. All metrics with the same name use
// one descriptor. The name must have the same help, type and label names in
// all profiles of the process, because Prometheus does not accept a
// difference.
func newMetric(deviceName string, spec profile.Metric, families map[string]family) (metric, error) {
	valueType := prometheus.GaugeValue
	if spec.Type == "counter" {
		valueType = prometheus.CounterValue
	}
	labelNames := append([]string{profile.DeviceLabel}, spec.LabelNames()...)
	fqName := namespace + "_" + spec.Name
	want := family{help: spec.Help, labels: strings.Join(labelNames, ","), valueType: valueType}
	m := metric{spec: spec, valueType: valueType, labels: []string{deviceName}}
	for _, name := range labelNames[1:] {
		if name != spec.ValueLabel {
			m.labels = append(m.labels, spec.Labels[name])
		}
	}
	if f, ok := families[fqName]; ok {
		if f.help != want.help || f.labels != want.labels || f.valueType != want.valueType {
			return metric{}, fmt.Errorf("metric %s has a different help text, type or label names in another profile", fqName)
		}
		m.desc = f.desc
		return m, nil
	}
	want.desc = prometheus.NewDesc(fqName, spec.Help, labelNames, nil)
	families[fqName] = want
	m.desc = want.desc
	return m, nil
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.descs {
		ch <- desc
	}
}

// Collect implements prometheus.Collector. Devices with different addresses
// are polled at the same time.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	var wg sync.WaitGroup
	for _, devices := range c.byAddress {
		wg.Go(func() {
			for _, d := range devices {
				c.collect(d, ch)
			}
		})
	}
	wg.Wait()
}

// current returns the last poll of the device. It polls the device first
// when the last poll is older than the poll interval. Only one poll of a
// device runs at a time, and a failed poll is also not repeated before the
// interval is over.
func (c *Collector) current(ctx context.Context, d *device) *poll {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last != nil && time.Since(d.last.at) < d.PollInterval {
		return d.last
	}
	begin := time.Now()
	result, err := d.Reader.Read(ctx, d.blocks)
	d.last = &poll{at: begin, duration: time.Since(begin), result: result, err: err}
	if err != nil {
		c.logger.Error("poll failed", "device", d.Name, "duration", d.last.duration, "err", err)
	}
	for name, blockErr := range result.Failed {
		c.logger.Warn("block not read", "device", d.Name, "block", name, "err", blockErr)
	}
	return d.last
}

func (c *Collector) collect(d *device, ch chan<- prometheus.Metric) {
	p := c.current(context.Background(), d)
	ch <- prometheus.MustNewConstMetric(pollTimeDesc, prometheus.GaugeValue, float64(p.at.UnixNano())/1e9, d.Name)
	if p.err == nil {
		ch <- prometheus.MustNewConstMetric(deviceInfoDesc, prometheus.GaugeValue, 1,
			d.Name, d.Profile.Name, d.Profile.Vendor, d.model(p.result.Words))
	}
	for _, g := range d.groups {
		success := 1.0
		if p.err != nil {
			success = 0
		}
		for _, name := range g.blocks {
			if _, failed := p.result.Failed[name]; failed {
				success = 0
			}
		}
		// Values are exported only when the poll was complete for this
		// collector. Old or partial values are not exported.
		if success == 1 {
			for _, m := range g.metrics {
				m.collect(d.Profile, p.result.Words, ch)
			}
		}
		ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, p.duration.Seconds(), d.Name, g.name)
		ch <- prometheus.MustNewConstMetric(scrapeSuccessDesc, prometheus.GaugeValue, success, d.Name, g.name)
	}
}

// model returns the model name of the device for modbus_device_info.
func (d *device) model(w modbus.Words) string {
	spec := d.Profile.Model
	if spec == nil {
		return ""
	}
	if spec.Value != "" {
		return spec.Value
	}
	raw, ok := w.Raw(spec.WireTable(), spec.Addr(), spec.Kind(), d.Profile.Order())
	if !ok {
		return "unknown"
	}
	return mapped(spec.Map, raw)
}

// mapped returns the name of a register value.
func mapped(names map[uint32]string, raw uint32) string {
	if name, ok := names[raw]; ok {
		return name
	}
	return fmt.Sprintf("unknown 0x%04X", raw)
}

// scaled multiplies value by scale. For a scale such as 0.1 or 0.001 it
// divides by 10 or 1000: the result is then the nearest number to the
// decimal value, for example 28.5 and not 28.500000000000004.
func scaled(value, scale float64) float64 {
	if scale > 0 && scale < 1 {
		if divisor := math.Round(1 / scale); math.Abs(divisor*scale-1) < 1e-9 {
			return value / divisor
		}
	}
	return value * scale
}

// number returns the value of s as a number. The second result is false
// when the value was not read or is the "no data" value of its type.
func number(p *profile.Profile, w modbus.Words, s profile.Source) (raw uint32, value float64, ok bool) {
	raw, ok = w.Raw(s.WireTable(), s.Addr(), s.Kind(), p.Order())
	if !ok || p.IsNoData(s.Kind(), raw) {
		return 0, 0, false
	}
	return raw, modbus.Float(s.Kind(), raw), true
}

// collect sends the sample of the metric, if its registers have a value.
func (m metric) collect(p *profile.Profile, w modbus.Words, ch chan<- prometheus.Metric) {
	spec := m.spec
	if spec.IsText() {
		text, ok := w.Text(spec.WireTable(), spec.Addr(), uint16(spec.Count))
		if ok {
			ch <- prometheus.MustNewConstMetric(m.desc, m.valueType, 1, append(m.labels[:len(m.labels):len(m.labels)], text)...)
		}
		return
	}
	raw, value, ok := number(p, w, spec.Source)
	if !ok {
		return
	}
	switch {
	case spec.ValueLabel != "":
		ch <- prometheus.MustNewConstMetric(m.desc, m.valueType, 1, append(m.labels[:len(m.labels):len(m.labels)], mapped(spec.Map, raw))...)
		return
	case spec.Bitmask != 0:
		value = 0
		if raw&spec.Bitmask != 0 {
			value = 1
		}
	default:
		value = scaled(value, spec.Scale)
		if spec.Multiply != nil {
			_, factor, ok := number(p, w, spec.Multiply.Source)
			if !ok {
				return
			}
			value *= scaled(factor, spec.Multiply.Scale)
		}
		switch spec.Part {
		case "positive":
			value = max(value, 0)
		case "negative":
			value = max(-value, 0)
		}
	}
	ch <- prometheus.MustNewConstMetric(m.desc, m.valueType, value, m.labels...)
}
