# modbus_exporter

A Prometheus exporter for devices that have Modbus TCP: solar inverters, batteries, energy meters, and others.
The exporter reads the registers of each device and exports the values on `/metrics`.

The exporter has no knowledge of a vendor or a type of device.
A device profile, which is a YAML file, says which registers to read and which metric each register makes.
The first profile is `sungrow_hybrid`, for Sungrow hybrid inverters with a battery. It is part of the binary.

The structure of the code and the flags are the same as in `node_exporter` and [`amber_exporter`](https://github.com/goodtune/amber_exporter).

## Safety

The exporter only reads.

- It uses Modbus function 0x04 (read input registers) and function 0x03 (read holding registers), and no other function.
- The code has no write path. The Modbus client is held through an interface that has only the two read functions. A test fails if a Go file names a write function of the Modbus library.
- A profile cannot make the exporter write. A profile selects a register table, `input` or `holding`, and the exporter has only a read function for each.
- It opens one connection for each poll of a device and closes the connection after the poll.
- It polls a device not more than one time in 10 seconds. A poll interval of less than 5 seconds is not accepted.
- Devices with the same address, for example behind one gateway, are polled one after the other.

`modbus_probe`, the register dump tool, uses the same two read functions. It also has no write path.

## Quick start

1. Get the exporter from the [releases page](https://github.com/goodtune/modbus_exporter/releases), or build it with `make build`.
2. Start the exporter with the address of the device and the name of a profile:

   ```sh
   ./modbus_exporter --modbus.address=192.0.2.10:502 --modbus.profile=sungrow_hybrid --modbus.device-name=inverter
   ```

3. Read the metrics at <http://localhost:10063/metrics>.

To poll more than one device, use a [configuration file](#configuration-file).

The directory `examples` has a configuration file and a Prometheus configuration.

## Install

Each release has these artifacts:

- Archives of the binaries for Linux and macOS, for `amd64` and `arm64`.
- Packages for Linux: `rpm`, `deb`, and `apk`.
- A container image: `ghcr.io/goodtune/modbus_exporter`.

### Linux package

The package installs the binary, a systemd unit, and the files `/etc/modbus_exporter/config.yml` and `/etc/modbus_exporter/environment`.
The unit is not enabled after the installation, because the exporter cannot start without a device.

1. Install the package.
2. Put the devices in `/etc/modbus_exporter/config.yml`.
3. To set flags, put them in `/etc/modbus_exporter/environment`, on the line `MODBUS_EXPORTER_ARGS=`.
4. Start the service:

   ```sh
   sudo systemctl enable --now modbus_exporter.service
   ```

If the configuration is not correct, the exporter stops with exit status 2, and systemd does not start it again.

### Container

```sh
docker run -p 10063:10063 ghcr.io/goodtune/modbus_exporter \
  --modbus.address=192.0.2.10:502 --modbus.profile=sungrow_hybrid
```

Note: Port 10063 is not registered in the Prometheus list of default port allocations.

## Flags

| Flag | Default | Function |
|---|---|---|
| `--web.listen-address` | `:10063` | Address for the web server. You can repeat the flag. |
| `--web.config.file` | none | File for TLS and basic authentication, in the `exporter-toolkit` format. |
| `--web.telemetry-path` | `/metrics` | Path of the metrics. |
| `--web.disable-exporter-metrics` | off | Remove the `go_*`, `process_*`, and `promhttp_*` metrics. |
| `--config.file` | none | Configuration file that lists the devices. |
| `--modbus.address` | none | `host:port` of one device or its Modbus TCP gateway. Use it when there is no configuration file. |
| `--modbus.profile` | none | Profile of the device of `--modbus.address`: an embedded profile or the path of a profile file. |
| `--modbus.unit-id` | `1` | Modbus unit id of the device of `--modbus.address`. |
| `--modbus.device-name` | name of the profile | Value of the `device` label for the device of `--modbus.address`. |
| `--modbus.poll-interval` | `10s` | Shortest time between two polls of a device. Not less than `5s`. |
| `--modbus.timeout` | `10s` | Timeout of one Modbus request. |
| `--modbus.request-pause` | `100ms` | Pause between two requests of one poll. |
| `--log.level`, `--log.format` | `info`, `logfmt` | Log settings. |

Give `--config.file`, or `--modbus.address` with `--modbus.profile`. The exporter does not accept the two together.

With a configuration file, `--modbus.poll-interval`, `--modbus.timeout` and `--modbus.request-pause` are the defaults for the devices that do not set their own value.

## Configuration file

The configuration file lists the devices. One exporter can poll many devices, and each device has its own poll.

```yaml
devices:
  - name: inverter
    address: 192.0.2.10:502
    unit_id: 1
    profile: sungrow_hybrid

  - name: garage
    address: 192.0.2.11:502
    profile: profiles/my_meter.yaml
    poll_interval: 30s
    timeout: 5s
    request_pause: 200ms
    collectors: [grid, energy]
```

| Key | Default | Function |
|---|---|---|
| `name` | name of the profile | Value of the `device` label of all metrics of the device. Each device must have a different name. |
| `address` | none, necessary | `host:port` of the device or its Modbus TCP gateway. |
| `unit_id` | `1` | Modbus unit id. |
| `profile` | none, necessary | An embedded profile, or the path of a profile file. A relative path starts at the directory of the configuration file. |
| `poll_interval` | `--modbus.poll-interval` | Shortest time between two polls. Not less than `5s`. |
| `timeout` | `--modbus.timeout` | Timeout of one Modbus request. |
| `request_pause` | `--modbus.request-pause` | Pause between two requests of one poll. |
| `collectors` | all | Collectors of the profile to enable. The exporter reads only the register blocks that the enabled collectors use. |

A key that the format does not have is an error.

## How the exporter polls

Prometheus can scrape more frequently than is good for a device.
Thus the exporter keeps the values of the last poll of each device.
When the last poll is older than the poll interval, the subsequent scrape starts a new poll.
A scrape that comes sooner gets the values of the last poll.

A profile divides the registers into blocks. One block is one Modbus request.

- If the device refuses a block with a Modbus exception, for example "illegal data address", only the collectors that use that block fail.
- All other errors stop the poll, and all collectors of the device fail.
- A collector that failed exports no values and sets `modbus_scrape_collector_success` to 0. The exporter does not export old or partial values.
- After a failed poll, the exporter waits for the poll interval before it tries again.

The values of one poll are not from the same instant, because the blocks are read one after the other.

## Metric names and labels

A metric name says what the quantity is, and not which device it comes from.
The identity of the device is in labels.

- **Prefix.** All metric names start with `modbus_`, the name of the exporter. This is the same rule as `node_` and `amber_`, and it prevents a conflict with the metrics of other exporters.
- **Name.** The rest of the name comes from the profile: a subsystem (`grid`, `pv`, `battery`, `load`, `inverter`), the quantity, and the base unit. For example `modbus_grid_power_watts`. The vendor and the model are not in the name.
- **Units.** Values are in base units: `watts`, `volts`, `amperes`, `hertz`, `celsius`, and `ratio` (0 to 1, not percent). The energy counters are in `watt_hours`, because that is the unit of the devices and of electricity prices.
- **The `device` label.** Each metric has the label `device`. Its value is the name that you give the device in the configuration. This is the only identity label on the values.
- **`modbus_device_info`.** One series for each device, with the labels `device`, `profile`, `vendor` and `model`. The value is 1. Join it with a value to select or to show devices by vendor or model.
- **Other labels.** A profile adds a label only where one quantity has more than one value: `phase`, `mppt`, `state`, `flow`, `component`.

The address of the device and serial numbers are not in labels.
Prometheus adds the `instance` and `job` labels of the exporter.

The cardinality is low: with the `sungrow_hybrid` profile, one device has approximately 80 series, and the number does not change.
The labels with a variable value are `state` (one series, for the current state) and `version` of `modbus_device_firmware_info` (a new series only after a firmware update).

Example series:

```
modbus_device_info{device="inverter",profile="sungrow_hybrid",vendor="Sungrow",model="SH15T"} 1
modbus_grid_power_watts{device="inverter"} -120
modbus_grid_voltage_volts{device="inverter",phase="a"} 240.5
modbus_pv_power_watts{device="inverter"} 3400
modbus_battery_state_of_charge_ratio{device="inverter"} 0.62
modbus_load_power_watts{device="inverter"} 980
modbus_grid_import_energy_watt_hours_total{device="inverter"} 1.234e+06
modbus_scrape_collector_success{device="inverter",collector="grid"} 1
```

To select by vendor or model, join with `modbus_device_info`:

```promql
modbus_battery_state_of_charge_ratio
* on (instance, job, device) group_left (vendor, model)
modbus_device_info
```

A second profile for a different device must use the same names for the same quantities.
Then one dashboard and one set of rules work for all devices.
If two profiles in one exporter give the same metric name a different help text, type or set of label names, the exporter does not start.

### Sign conventions

A profile must use these signs for the names below. The `sungrow_hybrid` profile does.

| Metric | Positive | Negative |
|---|---|---|
| `modbus_grid_power_watts`, `modbus_grid_phase_power_watts` | Import from the grid | Export to the grid |
| `modbus_grid_inverter_export_power_watts` | Export to the grid | Import from the grid |
| `modbus_battery_power_watts`, `modbus_battery_current_amperes` | Battery discharges | Battery charges |
| `modbus_inverter_active_power_watts` | Inverter supplies AC power | Inverter takes AC power |

For calculations with prices, use the metrics that are never negative:

- `modbus_grid_import_power_watts` and `modbus_grid_export_power_watts`
- `modbus_battery_charge_power_watts` and `modbus_battery_discharge_power_watts`

### Exporter metrics

| Metric | Labels | Description |
|---|---|---|
| `modbus_device_info` | `device`, `profile`, `vendor`, `model` | The device. The value is 1. It is exported when the poll got a connection. |
| `modbus_scrape_collector_success` | `device`, `collector` | 1 when the collector got all its registers in the last poll. |
| `modbus_scrape_collector_duration_seconds` | `device`, `collector` | Time of the last poll of the device. |
| `modbus_scrape_poll_timestamp_seconds` | `device` | Time of the last poll of the device. |
| `modbus_exporter_build_info` | `version`, `revision`, `branch`, `goversion`, `goos`, `goarch`, `tags` | The build of the exporter. The value is 1. |

## Device profiles

A profile is a YAML file. It describes one type of device.
The embedded profiles are in the directory [`profile/profiles`](profile/profiles).

To change an embedded profile, copy its file, change the copy, and give the path of the copy as the profile.
To add a device of a different type or vendor, write a new profile file. No Go code is necessary.

```yaml
name: example_meter            # value of the "profile" label
vendor: Example                # value of the "vendor" label
description: Example energy meter

address_offset: -1             # wire address = register number + address_offset
word_order: high_first         # sequence of the two registers of a 32-bit value
no_data: {u16: 0xFFFF}         # register values that mean "no data"

model:                         # value of the "model" label
  register: 1
  data_type: u16
  map: {0x0001: EM-100, 0x0002: EM-300}

collectors:                    # groups of metrics
  - {name: grid, help: Power and voltage at the meter.}

blocks:                        # one block is one read request
  - {name: live, table: input, start: 1, count: 20}

metrics:
  - name: grid_power_watts     # exported as modbus_grid_power_watts
    help: "Active power at the meter in W. Positive: import. Negative: export."
    collector: grid
    register: 11
    data_type: s32
  - name: grid_voltage_volts
    help: Grid voltage for each phase in V.
    collector: grid
    register: 13
    data_type: u16
    scale: 0.1
    labels: {phase: a}
```

Keys of the profile:

| Key | Default | Function |
|---|---|---|
| `name` | necessary | Name of the profile. Lower-case letters, digits and `_`. |
| `vendor`, `description` | none | Vendor of the device, and a text for people. |
| `address_offset` | `0` | Number that is added to each register number of the profile to get the address on the wire. Use `-1` when the document of the vendor starts to count at 1. |
| `word_order` | `high_first` | `high_first` or `low_first`: the sequence of the two registers of a 32-bit value. |
| `no_data` | none | For the data types `u16`, `s16`, `u32`, `s32`: the register value that means "no data". The exporter makes no sample from such a value. |
| `model` | none | A fixed `value`, or a `register` with a `data_type` and a `map` from register values to model names. |
| `collectors` | necessary | List of `name` and `help`. Each metric is part of one collector. |
| `blocks` | necessary | List of `name`, `table`, `start`, `count`. `count` is 1 to 125. Blocks of one table must not have registers in common. |
| `metrics` | necessary | List of metrics. See below. |

Keys of a metric:

| Key | Default | Function |
|---|---|---|
| `name` | necessary | Metric name without the `modbus_` prefix. |
| `help` | necessary | Help text. Give the unit and, for a value with a sign, the meaning of the sign. |
| `type` | `gauge` | `gauge` or `counter`. |
| `collector` | necessary | Collector that the metric is part of. |
| `table` | `input` | `input` (function 0x04) or `holding` (function 0x03). |
| `register` | necessary | Register number of the value. It must be in a block. |
| `data_type` | necessary | `u16`, `s16` (one register), `u32`, `s32`, `f32` (two registers), or `string`. |
| `count` | none | Number of registers of a `string`. |
| `scale` | `1` | Factor that changes the register value into the base unit. |
| `multiply` | none | A second value, with `register`, `data_type` and `scale`. The metric is the product of the two values. |
| `part` | none | `positive`: the value when it is more than 0, and 0 if not. `negative`: the value without its sign when it is less than 0, and 0 if not. |
| `bitmask` | none | The metric is 1 when a bit of the mask is set in the register value, and 0 if not. |
| `value_label` | none | Name of a label that gets the value. The sample value is then 1. The label value is the text of a `string`, or the name that `map` has for the register value. |
| `map` | none | Names of register values, for `value_label`. A value that is not in the map gives `unknown 0x....`. |
| `labels` | none | Fixed labels of the metric. |

Rules:

- A key that the format does not have is an error.
- Metrics with the same name must have the same help text, the same type and the same label names, and different label values.
- The label `device` and the names `device_info` and `scrape_*` are for the exporter.
- Follow the [Prometheus rules for metric names](https://prometheus.io/docs/practices/naming/), and use the names and the signs of the `sungrow_hybrid` profile for the same quantities.

Use `modbus_probe` to find and to check the registers of a new device.

## Profile `sungrow_hybrid`

For Sungrow residential hybrid inverters (SH series) with a Sungrow battery.
The Modbus TCP server is in the inverter or in its WiNet-S communication module.

The profile was verified against an SH15T (three-phase, 15 kW) with an SBR battery and a WiNet-S module.
Each register of the tables below was read from that system, and each value was checked against other values or against the model.

Protocol facts that the test found:

- **Plain Modbus TCP on port 502, unit id 1.** No encryption and no login were necessary.
- **Addresses are off by one.** The address on the wire is the register number of the Sungrow document minus 1. The profile has the register numbers of the document and `address_offset: -1`.
- **32-bit values have the low word first.**
- **A poll is fast.** Seven requests, 154 registers, approximately 0.7 s. Of that time, 0.6 s is the pause between requests. One request takes 6 to 15 ms.
- **More than one client is possible.** A second connection that stayed open did not stop the poll of the exporter.
- **The meter voltage and current registers are not there.** Registers 5741 to 5746 return the exception "illegal data address". The profile uses the phase voltages that the inverter measures.

### Registers

"Register" is the register number of the Sungrow document. Subtract 1 to get the address on the wire.
All registers are input registers (function 0x04).
"Scale" changes the register value into the unit of the metric.
All metrics also have the label `device`.

#### Collector `inverter`

| Register | Type | Scale | Metric | How it was checked |
|---|---|---|---|---|
| 5000 | U16 | – | `modbus_device_info{model}` | The device type code `0x0E25` is the SH15T. |
| 5001 | U16 | 100 | `modbus_inverter_rated_power_watts` | 15000 W, agrees with the model. |
| 5008 | S16 | 0.1 | `modbus_inverter_temperature_celsius` | A credible temperature. |
| 5035 | S16 | 0.001 | `modbus_inverter_power_factor` | 1.000 in steady operation. |
| 13000 | U16 | – | `modbus_inverter_running_state_code`, `modbus_inverter_running_state{state}` | `0x0000` = Running. |
| 13001 | U16 | bits | `modbus_inverter_power_flow{flow}` | The bits agreed with the sign of the PV, battery and load power. |
| 13031–13033 | S16 | 0.1 | `modbus_inverter_phase_current_amperes{phase}` | Agrees with the active power and the voltage. |
| 13034 | S32 | 1 | `modbus_inverter_active_power_watts` | See "Power balance". |
| 13250, 13265, 13280 | text, 15 registers each | – | `modbus_device_firmware_info{component,version}` | Readable firmware names of the inverter, the communication module and the battery. |

The values of `flow` are `pv_generating`, `battery_charging`, `battery_discharging`, `load_active`, `grid_exporting` and `grid_importing`.
The values of `component` are `inverter`, `communication_module` and `battery`.

#### Collector `pv`

| Register | Type | Scale | Metric | How it was checked |
|---|---|---|---|---|
| 5011, 5013, 5015 | U16 | 0.1 | `modbus_pv_mppt_voltage_volts{mppt}` | An MPPT that is not used reports 0. |
| 5012, 5014, 5016 | U16 | 0.1 | `modbus_pv_mppt_current_amperes{mppt}` | |
| 5017 | U32 | 1 | `modbus_pv_power_watts` | Agrees with the sum of volts times amperes of the MPPTs. |
| – | – | – | `modbus_pv_mppt_power_watts{mppt}` | The exporter calculates it: voltage times current. |

#### Collector `grid`

| Register | Type | Scale | Metric | How it was checked |
|---|---|---|---|---|
| 5019–5021 | U16 | 0.1 | `modbus_grid_voltage_volts{phase}` | Credible grid voltages. |
| 5242 | U16 | 0.01 | `modbus_grid_frequency_hertz` | Approximately 50 Hz. |
| 5601 | S32 | 1 | `modbus_grid_power_watts`, `modbus_grid_import_power_watts`, `modbus_grid_export_power_watts` | See "Sign of the grid power". |
| 5603, 5605, 5607 | S32 | 1 | `modbus_grid_phase_power_watts{phase}` | The sum agrees with register 5601. |
| 13010 | S32 | 1 | `modbus_grid_inverter_export_power_watts` | The same value as register 5601 with the opposite sign. |

#### Collector `load`

| Register | Type | Scale | Metric | How it was checked |
|---|---|---|---|---|
| 13008 | S32 | 1 | `modbus_load_power_watts` | See "Power balance". |

#### Collector `battery`

| Register | Type | Scale | Metric | How it was checked |
|---|---|---|---|---|
| 5214 | S32 | 1 | `modbus_battery_power_watts`, `modbus_battery_charge_power_watts`, `modbus_battery_discharge_power_watts` | Positive while the power flow status said "discharging". |
| 5631 | S16 | 0.1 | `modbus_battery_current_amperes` | Agrees with the power divided by the voltage. |
| 5635, 5636 | U16 | 1 | `modbus_battery_max_charge_current_amperes`, `modbus_battery_max_discharge_current_amperes` | |
| 5639 | U16 | 10 | `modbus_battery_capacity_watt_hours` | Agrees with the rated capacity of the battery. |
| 13020 | U16 | 0.1 | `modbus_battery_voltage_volts` | A credible voltage for a high-voltage battery. |
| 13023 | U16 | 0.001 | `modbus_battery_state_of_charge_ratio` | Decreased slowly during discharge. |
| 13024 | U16 | 0.001 | `modbus_battery_state_of_health_ratio` | |
| 13025 | S16 | 0.1 | `modbus_battery_temperature_celsius` | |

#### Collector `energy`

Each quantity has a counter for today (U16) and, directly after it, a lifetime counter (U32).
The scale of all is 100 (0.1 kWh to Wh).
The metric names end in `_today_watt_hours` (gauge) and `_watt_hours_total` (counter).

| Register today | Register lifetime | Metric prefix |
|---|---|---|
| 13002 | 13003 | `modbus_pv_energy` |
| 13005 | 13006 | `modbus_pv_export_energy` |
| 13012 | 13013 | `modbus_battery_charge_from_pv_energy` |
| 13017 | 13018 | `modbus_pv_direct_consumption_energy` |
| 13026 | 13027 | `modbus_battery_discharge_energy` |
| 13036 | 13037 | `modbus_grid_import_energy` |
| 13040 | 13041 | `modbus_battery_charge_energy` |
| 13045 | 13046 | `modbus_grid_export_energy` |

The counters increased by 0.1 kWh steps during the test, at a rate that agrees with the power values.

### Power balance

The values of one poll agree with each other:

- PV power plus battery discharge power is the power on the DC side.
- The AC output of the inverter was 90 to 92 % of the DC power, a credible efficiency.
- The load power was the inverter output plus the grid import, with a difference of some tens of watts. The registers are not read at the same instant.

### Sign of the grid power

The Sungrow document says that register 5601 is positive for import ("buy from grid") and negative for export.
The profile uses that convention. The test gave these results:

- **Verified:** register 13010 (export power that the inverter calculates) is the same value as register 5601 with the opposite sign. Most samples agreed to the watt.
- **Verified:** when a large load started, register 5601 was positive while the inverter output increased to follow the load. A positive value was thus an import.
- **Verified:** when a large load stopped, register 5601 was negative while the inverter output was still more than the load. A negative value was thus an export.
- **Verified:** with grid power near 0, the import and export counters of today did not change.
- **Not verified:** a continuous import or export of some hundred watts. During the test the battery kept the grid power near 0, apart from transients of one sample.

Before you use the sign for money, compare `modbus_grid_power_watts` with the app of the vendor during a clear import, for example when the battery charges from the grid.

### Not verified, or not correct

- **Sign of the grid power during a continuous import or export.** See above.
- **Battery charge.** Only discharge was seen. The negative sign of `modbus_battery_power_watts` and `modbus_battery_current_amperes` during charge comes from the document only.
- **Running states other than "Running"**, and device types other than SH15T. The tables come from the sources only.
- **PV power is coarse.** Register 5017 changes in steps of approximately 50 W, and the inverter updates it slowly.
- **Battery current is slow.** Register 5631 can stay at the same value for some minutes while the battery power changes.
- **The energy counters are coarse.** Their resolution is 0.1 kWh.
- **Fast load changes.** A fast load change can give a grid power that does not agree with the load power of the same poll, for one poll.
- **Meter voltage and current** (5741 to 5746) and **MPPT 4** (5115, 5116) are not exported. The meter registers do not exist on the test device. MPPT 4 was not read.
- **Backup power** (5723 to 5727) can be read. It is not exported, because the test system has no known backup circuit.
- **The night.** The test did not run for a full night. It is not known whether the communication module answers in the same way when there is no PV power for hours.
- **Long operation.** The test ran for approximately 30 minutes with polls at intervals of 10 s. No poll failed. It is not known how the communication module operates during days or weeks.

### Sources for the register map

Each register was first found in these sources and then checked on the live device:

- `mkaiser/Sungrow-SHx-Inverter-Modbus-Home-Assistant`, file `modbus_sungrow.yaml`.
  The file refers to the Sungrow document "Communication Protocol of Residential Hybrid Inverter".
- `bohdan-s/SunGather`, file `registers-sungrow.yaml`. It uses the register numbers of the document and agrees with the first source.

## Register dump tool

`modbus_probe` reads blocks of registers and prints each register as hexadecimal, unsigned, signed and text.

```sh
make probe
./modbus_probe --address=192.0.2.10:502 4999:2 12999:48
./modbus_probe --address=192.0.2.10:502 --holding 0:10
```

Each argument is `START:COUNT`. `START` is the address on the wire, and `COUNT` is 1 to 125.
The tool reads input registers. With `--holding` it reads holding registers.

Stop the exporter before you use the tool, if the device accepts only one client.

## Queries

Net power from the grid, for all devices:

```promql
sum(modbus_grid_power_watts)
```

Part of the load that the grid supplies:

```promql
modbus_grid_import_power_watts / modbus_load_power_watts
```

Energy that was imported from the grid in the last 24 hours, in kWh:

```promql
increase(modbus_grid_import_energy_watt_hours_total[24h]) / 1000
```

Devices that the exporter cannot read:

```promql
modbus_scrape_collector_success == 0
```

With the price from [`amber_exporter`](https://github.com/goodtune/amber_exporter), the cost rate of the grid import in cents for each hour is:

```promql
modbus_grid_import_power_watts / 1000 * on () group_left () amber_price_cents_per_kwh{channel_type="general"}
```

Use the power metrics for this, and not the energy counters: the counters have a resolution of only 0.1 kWh.
This query was not tested.

## Development

```sh
make all            # format check, vet, tests, build
make probe          # build the register dump tool
make lint           # golangci-lint
make release-check  # validate .goreleaser.yml
make snapshot       # build all release artifacts into dist/, publish nothing
```

The tests do not use the network and do not need a device.
They use a Modbus TCP server and a register reader that are part of the tests, with register values that are not from a real system.

The packages:

| Package | Function |
|---|---|
| `modbus` | Read-only Modbus TCP client, and the decoding of register values. |
| `profile` | The profile format, its validation, and the embedded profiles. |
| `config` | The configuration file. |
| `collector` | Makes the metrics of each device from its profile and its register values. |
| `cmd/modbus_probe` | The register dump tool. |

## Release

[GoReleaser](https://goreleaser.com) builds and publishes the releases.

1. Publish a GitHub release with a tag in the format `v1.2.3`.
2. The workflow `Release` then runs GoReleaser.

GoReleaser adds the archives, the packages, and the checksums to the GitHub release.
It also pushes the container image to `ghcr.io/goodtune/modbus_exporter` with the tag of the release and the tag `latest`.
