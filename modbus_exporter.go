// Command modbus_exporter exports the register values of Modbus TCP devices
// as Prometheus metrics. A device profile says which registers to read. The
// exporter only reads: it never writes to a device.
package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/goodtune/modbus_exporter/collector"
	"github.com/goodtune/modbus_exporter/config"
	"github.com/goodtune/modbus_exporter/modbus"
	"github.com/goodtune/modbus_exporter/profile"
)

const exporterName = "modbus_exporter"

// exitConfig is the exit status for a configuration that is not correct.
// The systemd unit does not start the exporter again after this status.
const exitConfig = 2

func main() {
	setDefaultVersion()
	os.Exit(run())
}

// setDefaultVersion gives the build a version when linker flags did not set
// one, as with a plain "go build".
func setDefaultVersion() {
	if version.Version != "" {
		return
	}
	version.Version = "unknown"
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		version.Version = info.Main.Version
	}
}

// settings are the flags that select the devices.
type settings struct {
	configFile string
	address    string
	unitID     uint8
	profile    string
	deviceName string
	defaults   config.Defaults
}

// devices returns the devices of the configuration file, or the one device
// of the --modbus.* flags.
func (s settings) devices() ([]config.Device, string, error) {
	if s.configFile != "" {
		if s.address != "" || s.profile != "" {
			return nil, "", errors.New("use --config.file, or --modbus.address with --modbus.profile, and not the two")
		}
		c, err := config.Load(s.configFile, s.defaults)
		if err != nil {
			return nil, "", err
		}
		return c.Devices, filepath.Dir(s.configFile), nil
	}
	if s.address == "" || s.profile == "" {
		return nil, "", errors.New("give --config.file, or --modbus.address with --modbus.profile")
	}
	c := &config.Config{Devices: []config.Device{{
		Name: s.deviceName, Address: s.address, UnitID: &s.unitID, Profile: s.profile,
	}}}
	if err := c.Validate(s.defaults); err != nil {
		return nil, "", err
	}
	return c.Devices, "", nil
}

// newCollector loads the profile of each device and makes the collector.
func newCollector(devices []config.Device, baseDir string, logger *slog.Logger) (*collector.Collector, error) {
	profiles := map[string]*profile.Profile{}
	var targets []collector.Device
	for _, d := range devices {
		p, ok := profiles[d.Profile]
		if !ok {
			var err error
			if p, err = profile.Load(d.Profile, baseDir); err != nil {
				return nil, err
			}
			profiles[d.Profile] = p
		}
		name := d.Name
		if name == "" {
			name = p.Name
		}
		logger.Info("Device", "device", name, "address", d.Address, "profile", p.Name, "unit_id", d.Unit(),
			"poll_interval", d.PollInterval, "blocks", len(p.Blocks), "metrics", len(p.Metrics))
		targets = append(targets, collector.Device{
			Name:         name,
			Address:      d.Address,
			Profile:      p,
			Reader:       modbus.NewClient(d.Address, d.Unit(), d.Timeout, d.RequestPause),
			PollInterval: d.PollInterval,
			Collectors:   d.Collectors,
		})
	}
	return collector.New(targets, logger)
}

func run() int {
	app := kingpin.New(exporterName, "Prometheus exporter for Modbus TCP devices. A device profile says which registers to read. The exporter only reads, it never writes.")
	var s settings
	var (
		metricsPath = app.Flag("web.telemetry-path", "Path under which to expose metrics.").
				Default("/metrics").String()
		disableExporterMetrics = app.Flag("web.disable-exporter-metrics", "Exclude metrics about the exporter itself (promhttp_*, process_*, go_*).").
					Bool()
		toolkitFlags = kingpinflag.AddFlags(app, ":10063")
	)
	app.Flag("config.file", "Configuration file that lists the devices. Use it to poll more than one device.").
		StringVar(&s.configFile)
	app.Flag("modbus.address", "Address of one device or its Modbus TCP gateway, as host:port. The usual Modbus TCP port is 502.").
		StringVar(&s.address)
	app.Flag("modbus.unit-id", "Modbus unit id of the device of --modbus.address.").
		Default("1").Uint8Var(&s.unitID)
	app.Flag("modbus.profile", "Profile of the device of --modbus.address: the path of a profile file, or an embedded profile ("+strings.Join(profile.Embedded(), ", ")+").").
		StringVar(&s.profile)
	app.Flag("modbus.device-name", "Value of the device label for the device of --modbus.address. The default is the name of the profile.").
		StringVar(&s.deviceName)
	app.Flag("modbus.poll-interval", "Shortest time between two polls of a device, "+config.MinPollInterval.String()+" or more. A device in the configuration file can set its own value.").
		Default("10s").DurationVar(&s.defaults.PollInterval)
	app.Flag("modbus.timeout", "Timeout of one Modbus request. A device in the configuration file can set its own value.").
		Default("10s").DurationVar(&s.defaults.Timeout)
	app.Flag("modbus.request-pause", "Pause between two Modbus requests of one poll. A device in the configuration file can set its own value.").
		Default("100ms").DurationVar(&s.defaults.RequestPause)

	promslogConfig := &promslog.Config{}
	flag.AddFlags(app, promslogConfig)
	app.Version(version.Print(exporterName))
	app.HelpFlag.Short('h')
	kingpin.MustParse(app.Parse(os.Args[1:]))
	logger := promslog.New(promslogConfig)

	logger.Info("Starting "+exporterName, "version", version.Info())
	logger.Info("Build context", "build_context", version.BuildContext())

	devices, baseDir, err := s.devices()
	if err != nil {
		logger.Error("The configuration is not correct", "err", err)
		return exitConfig
	}
	modbusCollector, err := newCollector(devices, baseDir, logger)
	if err != nil {
		logger.Error("The configuration is not correct", "err", err)
		return exitConfig
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(modbusCollector, versioncollector.NewCollector(exporterName))
	handlerOpts := promhttp.HandlerOpts{
		ErrorLog:      slog.NewLogLogger(logger.Handler(), slog.LevelError),
		ErrorHandling: promhttp.ContinueOnError,
	}
	var metricsHandler http.Handler
	if *disableExporterMetrics {
		metricsHandler = promhttp.HandlerFor(registry, handlerOpts)
	} else {
		registry.MustRegister(
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			collectors.NewGoCollector(),
		)
		handlerOpts.Registry = registry
		metricsHandler = promhttp.InstrumentMetricHandler(registry, promhttp.HandlerFor(registry, handlerOpts))
	}

	mux := http.NewServeMux()
	mux.Handle(*metricsPath, metricsHandler)
	if *metricsPath != "/" {
		landingPage, err := web.NewLandingPage(web.LandingConfig{
			Name:        "Modbus Exporter",
			Description: "Prometheus exporter for Modbus TCP devices",
			Version:     version.Info(),
			Links:       []web.LandingLinks{{Address: *metricsPath, Text: "Metrics"}},
		})
		if err != nil {
			logger.Error("Cannot create the landing page", "err", err)
			return 1
		}
		mux.Handle("/", landingPage)
	}

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := web.ListenAndServe(server, toolkitFlags, logger); err != nil {
		logger.Error("The web server stopped", "err", err)
		return 1
	}
	return 0
}
