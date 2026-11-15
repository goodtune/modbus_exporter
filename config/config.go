// Package config reads the configuration file of the exporter. The file
// lists the devices that the exporter polls.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// MinPollInterval is the shortest permitted time between two polls of a
// device. A device must not be polled faster than this.
const MinPollInterval = 5 * time.Second

// Config is the content of the configuration file.
type Config struct {
	Devices []Device `yaml:"devices"`
}

// Device is one Modbus device that the exporter polls.
type Device struct {
	// Name is the value of the device label of all metrics of the device.
	// Without a name, the exporter uses the name of the profile.
	Name string `yaml:"name"`
	// Address is the host:port of the device or of its Modbus TCP gateway.
	Address string `yaml:"address"`
	// UnitID is the Modbus unit id. The default is 1.
	UnitID *uint8 `yaml:"unit_id"`
	// Profile is the name of an embedded profile or the path of a profile
	// file. A relative path starts at the directory of the configuration
	// file.
	Profile string `yaml:"profile"`
	// PollInterval is the shortest time between two polls of the device.
	PollInterval time.Duration `yaml:"poll_interval"`
	// Timeout is the timeout of one Modbus request.
	Timeout time.Duration `yaml:"timeout"`
	// RequestPause is the pause between two Modbus requests of one poll.
	RequestPause time.Duration `yaml:"request_pause"`
	// Collectors lists the collectors of the profile to enable. Without a
	// list, all collectors are enabled.
	Collectors []string `yaml:"collectors"`
}

// Defaults are the values for the settings that a device does not set.
type Defaults struct {
	PollInterval time.Duration
	Timeout      time.Duration
	RequestPause time.Duration
}

// Unit returns the Modbus unit id of the device.
func (d Device) Unit() uint8 {
	if d.UnitID == nil {
		return 1
	}
	return *d.UnitID
}

// Load reads and validates the configuration file at path.
func Load(path string, defaults Defaults) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data, defaults)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse reads a configuration from YAML and validates it. A field that the
// format does not have is an error.
func Parse(data []byte, defaults Defaults) (*Config, error) {
	c := &Config{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(c); err != nil {
		return nil, err
	}
	if err := c.Validate(defaults); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate applies the defaults and reports settings that are not correct
// or not safe for a device.
func (c *Config) Validate(defaults Defaults) error {
	if len(c.Devices) == 0 {
		return errors.New("the configuration has no devices")
	}
	for i := range c.Devices {
		d := &c.Devices[i]
		if d.PollInterval == 0 {
			d.PollInterval = defaults.PollInterval
		}
		if d.Timeout == 0 {
			d.Timeout = defaults.Timeout
		}
		if d.RequestPause == 0 {
			d.RequestPause = defaults.RequestPause
		}
		what := fmt.Sprintf("device %d", i+1)
		if d.Name != "" {
			what = fmt.Sprintf("device %q", d.Name)
		}
		if _, _, err := net.SplitHostPort(d.Address); err != nil {
			return fmt.Errorf("%s: address must be host:port: %w", what, err)
		}
		if d.Profile == "" {
			return fmt.Errorf("%s: no profile is given", what)
		}
		if d.PollInterval < MinPollInterval {
			return fmt.Errorf("%s: the poll interval must be %s or more", what, MinPollInterval)
		}
		if d.Timeout <= 0 || d.RequestPause < 0 {
			return fmt.Errorf("%s: the timeout must be more than 0 and the request pause must not be negative", what)
		}
	}
	return nil
}
