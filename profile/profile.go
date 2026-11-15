// Package profile loads device profiles. A profile is a data file that says
// which registers of a device to read and which metric each register makes.
//
// A profile can only make the exporter read. It selects a register table
// ("input" or "holding"), and each of the two has only a read function.
package profile

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/goodtune/modbus_exporter/modbus"
)

//go:embed profiles/*.yaml
var embedded embed.FS

// DeviceLabel is the label that the exporter adds to each metric. A profile
// cannot use it.
const DeviceLabel = "device"

// Profile describes the registers of one type of device.
type Profile struct {
	// Name identifies the profile. It is the value of the profile label of
	// modbus_device_info.
	Name string `yaml:"name"`
	// Vendor is the value of the vendor label of modbus_device_info.
	Vendor      string `yaml:"vendor"`
	Description string `yaml:"description"`

	// AddressOffset is added to each register number of the profile to get
	// the address on the wire. Set it to -1 when the document of the vendor
	// starts to count at 1.
	AddressOffset int `yaml:"address_offset"`
	// WordOrder is the sequence of the two registers of a 32-bit value:
	// "high_first" (the default) or "low_first".
	WordOrder string `yaml:"word_order"`
	// NoData gives, for a data type, the register value that means "no
	// data". The exporter makes no sample from such a value.
	NoData map[string]uint32 `yaml:"no_data"`

	// Model says how to get the model label of modbus_device_info.
	Model      *Model      `yaml:"model"`
	Collectors []Collector `yaml:"collectors"`
	Blocks     []Block     `yaml:"blocks"`
	Metrics    []Metric    `yaml:"metrics"`

	order  modbus.WordOrder
	noData map[modbus.Kind]uint32
}

// Collector is a named group of metrics. The exporter reports for each
// collector whether its registers were read.
type Collector struct {
	Name string `yaml:"name"`
	Help string `yaml:"help"`
}

// Block is a range of registers that the exporter reads with one request.
type Block struct {
	Name  string `yaml:"name"`
	Table string `yaml:"table"`
	// Start is the register number of the first register, before the
	// address offset is added.
	Start int `yaml:"start"`
	Count int `yaml:"count"`

	wire modbus.Block
}

// Wire returns the block with its address on the wire.
func (b Block) Wire() modbus.Block { return b.wire }

// Source is the location and the data type of one value.
type Source struct {
	// Table is "input" (the default) or "holding".
	Table string `yaml:"table"`
	// Register is the register number, before the address offset is added.
	Register int `yaml:"register"`
	// DataType is u16, s16, u32, s32, f32 or string.
	DataType string `yaml:"data_type"`
	// Count is the number of registers of a string.
	Count int `yaml:"count"`

	table modbus.Table
	addr  uint16
	kind  modbus.Kind
	text  bool
	block string
}

// Table, Addr and Kind are valid after the profile was validated.

// WireTable returns the register table of the value.
func (s Source) WireTable() modbus.Table { return s.table }

// Addr returns the address of the value on the wire.
func (s Source) Addr() uint16 { return s.addr }

// Kind returns the data type of a value that is not text.
func (s Source) Kind() modbus.Kind { return s.kind }

// IsText reports whether the value is text.
func (s Source) IsText() bool { return s.text }

// BlockName returns the name of the block that holds the value.
func (s Source) BlockName() string { return s.block }

// Operand is a second value that a metric is multiplied by.
type Operand struct {
	Source `yaml:",inline"`
	Scale  float64 `yaml:"scale"`
}

// Model says where the model name of the device comes from: a fixed Value,
// or a register with a Map from its values to model names.
type Model struct {
	Value  string `yaml:"value"`
	Source `yaml:",inline"`
	Map    map[uint32]string `yaml:"map"`
}

// Metric is one metric that the exporter makes from one register value.
type Metric struct {
	// Name is the metric name without the "modbus_" prefix.
	Name string `yaml:"name"`
	Help string `yaml:"help"`
	// Type is "gauge" (the default) or "counter".
	Type string `yaml:"type"`
	// Collector is the name of the collector that the metric is part of.
	Collector string `yaml:"collector"`

	Source `yaml:",inline"`

	// Scale changes the register value into the base unit of the metric.
	// The default is 1.
	Scale float64 `yaml:"scale"`
	// Multiply gives a second value. The metric is the product of the two.
	Multiply *Operand `yaml:"multiply"`
	// Part keeps one side of a value that has a sign. "positive" gives the
	// value when it is more than 0, and 0 if not. "negative" gives the value
	// without its sign when it is less than 0, and 0 if not.
	Part string `yaml:"part"`
	// Bitmask makes the metric 1 when a bit of the mask is set in the
	// register value, and 0 if not.
	Bitmask uint32 `yaml:"bitmask"`

	// ValueLabel puts the value in a label with this name, and the sample
	// value is then 1. The label value is the text of a string, or the name
	// that Map has for the register value.
	ValueLabel string            `yaml:"value_label"`
	Map        map[uint32]string `yaml:"map"`

	// Labels are fixed labels of the metric.
	Labels map[string]string `yaml:"labels"`
}

// LabelNames returns the label names of the metric, without the device
// label: the fixed labels in alphabetical sequence, then the value label.
func (m Metric) LabelNames() []string {
	names := make([]string, 0, len(m.Labels)+1)
	for name := range m.Labels {
		names = append(names, name)
	}
	sort.Strings(names)
	if m.ValueLabel != "" {
		names = append(names, m.ValueLabel)
	}
	return names
}

// Order returns the word order of the 32-bit values.
func (p *Profile) Order() modbus.WordOrder { return p.order }

// IsNoData reports whether raw is the "no data" value of its data type.
func (p *Profile) IsNoData(kind modbus.Kind, raw uint32) bool {
	marker, ok := p.noData[kind]
	return ok && raw == marker
}

// Embedded returns the names of the profiles that are part of the binary.
func Embedded() []string {
	entries, err := fs.ReadDir(embedded, "profiles")
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	return names
}

// Load returns the profile that ref names. ref is the name of an embedded
// profile or the path of a profile file. A relative path starts at baseDir.
func Load(ref, baseDir string) (*Profile, error) {
	if ref == "" {
		return nil, errors.New("no profile is given")
	}
	source := "embedded profile " + ref
	data, err := embedded.ReadFile("profiles/" + ref + ".yaml")
	if err != nil {
		path := ref
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		source = path
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("profile %q is not an embedded profile (%s) and not a file: %w",
				ref, strings.Join(Embedded(), ", "), err)
		}
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return p, nil
}

// Parse reads a profile from YAML and validates it. A field that the profile
// format does not have is an error.
func Parse(data []byte) (*Profile, error) {
	p := &Profile{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(p); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return p, nil
}

var (
	nameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	labelRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

var kinds = map[string]modbus.Kind{
	"u16": modbus.U16, "s16": modbus.S16, "u32": modbus.U32, "s32": modbus.S32, "f32": modbus.F32,
}

func parseTable(name string) (modbus.Table, error) {
	switch name {
	case "", "input":
		return modbus.Input, nil
	case "holding":
		return modbus.Holding, nil
	}
	return 0, fmt.Errorf("table %q is not \"input\" or \"holding\"", name)
}

// validate checks the profile and calculates the addresses on the wire.
func (p *Profile) validate() error {
	if !nameRE.MatchString(p.Name) {
		return fmt.Errorf("profile name %q must match %s", p.Name, nameRE)
	}
	switch p.WordOrder {
	case "", "high_first":
		p.order = modbus.HighWordFirst
	case "low_first":
		p.order = modbus.LowWordFirst
	default:
		return fmt.Errorf("word_order %q is not \"high_first\" or \"low_first\"", p.WordOrder)
	}
	p.noData = map[modbus.Kind]uint32{}
	for name, marker := range p.NoData {
		kind, ok := kinds[name]
		if !ok || kind == modbus.F32 {
			return fmt.Errorf("no_data: %q is not one of u16, s16, u32, s32", name)
		}
		if kind.Words() == 1 && marker > 0xFFFF {
			return fmt.Errorf("no_data: the value of %s is more than 16 bits", name)
		}
		p.noData[kind] = marker
	}

	collectors := map[string]bool{}
	for _, c := range p.Collectors {
		if !nameRE.MatchString(c.Name) {
			return fmt.Errorf("collector name %q must match %s", c.Name, nameRE)
		}
		if collectors[c.Name] {
			return fmt.Errorf("collector %q is defined two times", c.Name)
		}
		collectors[c.Name] = true
	}
	if len(collectors) == 0 {
		return errors.New("the profile has no collectors")
	}

	if err := p.validateBlocks(); err != nil {
		return err
	}

	if p.Model != nil {
		if (p.Model.Value != "") == (p.Model.DataType != "" || p.Model.Register != 0 || len(p.Model.Map) > 0) {
			return errors.New("model: give a value, or a register with a map, and not the two")
		}
		if p.Model.Value == "" {
			if err := p.resolve(&p.Model.Source); err != nil {
				return fmt.Errorf("model: %w", err)
			}
			if err := checkMap(p.Model.Source, p.Model.Map); err != nil {
				return fmt.Errorf("model: %w", err)
			}
		}
	}

	if len(p.Metrics) == 0 {
		return errors.New("the profile has no metrics")
	}
	type family struct {
		help, kind string
		labels     string
	}
	families := map[string]family{}
	series := map[string]bool{}
	for i := range p.Metrics {
		m := &p.Metrics[i]
		if err := p.validateMetric(m, collectors); err != nil {
			return fmt.Errorf("metric %q (register %d): %w", m.Name, m.Register, err)
		}
		f := family{help: m.Help, kind: m.Type, labels: strings.Join(m.LabelNames(), ",")}
		if first, ok := families[m.Name]; ok && first != f {
			return fmt.Errorf("metric %q is defined with different help, type or label names", m.Name)
		}
		families[m.Name] = f
		id := m.Name
		for _, name := range m.LabelNames() {
			id += "\xff" + m.Labels[name]
		}
		if series[id] {
			return fmt.Errorf("metric %q is defined two times with the same labels", m.Name)
		}
		series[id] = true
	}
	return nil
}

func (p *Profile) validateBlocks() error {
	if len(p.Blocks) == 0 {
		return errors.New("the profile has no blocks")
	}
	names := map[string]bool{}
	for i := range p.Blocks {
		b := &p.Blocks[i]
		if !nameRE.MatchString(b.Name) {
			return fmt.Errorf("block name %q must match %s", b.Name, nameRE)
		}
		if names[b.Name] {
			return fmt.Errorf("block %q is defined two times", b.Name)
		}
		names[b.Name] = true
		table, err := parseTable(b.Table)
		if err != nil {
			return fmt.Errorf("block %q: %w", b.Name, err)
		}
		if b.Count < 1 || b.Count > modbus.MaxBlockCount {
			return fmt.Errorf("block %q: count %d is not from 1 to %d", b.Name, b.Count, modbus.MaxBlockCount)
		}
		start := b.Start + p.AddressOffset
		if start < 0 || start+b.Count-1 > 0xFFFF {
			return fmt.Errorf("block %q: the addresses %d to %d are not from 0 to 65535", b.Name, start, start+b.Count-1)
		}
		b.wire = modbus.Block{Name: b.Name, Table: table, Start: uint16(start), Count: uint16(b.Count)}
		for _, other := range p.Blocks[:i] {
			o := other.wire
			if o.Table == table && int(o.Start) < start+b.Count && start < int(o.Start)+int(o.Count) {
				return fmt.Errorf("blocks %q and %q have registers in common", other.Name, b.Name)
			}
		}
	}
	return nil
}

// resolve calculates the address on the wire of s and finds its block.
func (p *Profile) resolve(s *Source) error {
	table, err := parseTable(s.Table)
	if err != nil {
		return err
	}
	s.table = table
	words := 0
	if s.DataType == "string" {
		s.text = true
		if s.Count < 1 {
			return errors.New("a string needs a count of 1 or more")
		}
		words = s.Count
	} else {
		kind, ok := kinds[s.DataType]
		if !ok {
			return fmt.Errorf("data_type %q is not one of u16, s16, u32, s32, f32, string", s.DataType)
		}
		if s.Count != 0 {
			return errors.New("count is only for a string")
		}
		s.kind = kind
		words = int(kind.Words())
	}
	addr := s.Register + p.AddressOffset
	for _, b := range p.Blocks {
		w := b.wire
		if w.Table == table && addr >= int(w.Start) && addr+words <= int(w.Start)+int(w.Count) {
			s.addr = uint16(addr)
			s.block = b.Name
			return nil
		}
	}
	return fmt.Errorf("the %s registers %d to %d are not in one block", table, s.Register, s.Register+words-1)
}

func isInteger(s Source) bool { return !s.text && s.kind != modbus.F32 }

func checkMap(s Source, values map[uint32]string) error {
	if len(values) == 0 {
		return errors.New("the map is empty")
	}
	if !isInteger(s) {
		return errors.New("a map needs the data type u16, s16, u32 or s32")
	}
	return nil
}

func (p *Profile) validateMetric(m *Metric, collectors map[string]bool) error {
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("the name must match %s", nameRE)
	}
	if m.Name == "device_info" || strings.HasPrefix(m.Name, "scrape_") {
		return errors.New("the name is reserved for the exporter")
	}
	if strings.TrimSpace(m.Help) == "" {
		return errors.New("the help text is empty")
	}
	switch m.Type {
	case "":
		m.Type = "gauge"
	case "gauge", "counter":
	default:
		return fmt.Errorf("type %q is not \"gauge\" or \"counter\"", m.Type)
	}
	if !collectors[m.Collector] {
		return fmt.Errorf("collector %q is not defined", m.Collector)
	}
	if err := p.resolve(&m.Source); err != nil {
		return err
	}
	for name := range m.Labels {
		if !labelRE.MatchString(name) || name == DeviceLabel {
			return fmt.Errorf("label name %q is not permitted", name)
		}
	}
	if m.Scale == 0 {
		m.Scale = 1
	}

	arithmetic := m.Scale != 1 || m.Multiply != nil || m.Part != ""
	switch {
	case m.ValueLabel != "":
		if !labelRE.MatchString(m.ValueLabel) || m.ValueLabel == DeviceLabel {
			return fmt.Errorf("value_label %q is not permitted", m.ValueLabel)
		}
		if _, ok := m.Labels[m.ValueLabel]; ok {
			return fmt.Errorf("value_label %q is also a fixed label", m.ValueLabel)
		}
		if m.Type != "gauge" || arithmetic || m.Bitmask != 0 {
			return errors.New("a metric with value_label is a gauge with no scale, multiply, part or bitmask")
		}
		if m.text {
			if len(m.Map) > 0 {
				return errors.New("a string has no map")
			}
		} else if err := checkMap(m.Source, m.Map); err != nil {
			return err
		}
	case m.text:
		return errors.New("a string needs value_label")
	case len(m.Map) > 0:
		return errors.New("a map needs value_label")
	case m.Bitmask != 0:
		if !isInteger(m.Source) || m.Type != "gauge" || arithmetic {
			return errors.New("a bitmask needs an integer gauge with no scale, multiply or part")
		}
	}

	if m.Part != "" && m.Part != "positive" && m.Part != "negative" {
		return fmt.Errorf("part %q is not \"positive\" or \"negative\"", m.Part)
	}
	if m.Multiply != nil {
		if err := p.resolve(&m.Multiply.Source); err != nil {
			return fmt.Errorf("multiply: %w", err)
		}
		if m.Multiply.text {
			return errors.New("multiply: a string is not a number")
		}
		if m.Multiply.Scale == 0 {
			m.Multiply.Scale = 1
		}
	}
	return nil
}
