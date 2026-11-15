#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
    # The unit ships disabled: the exporter cannot start without a device.
    # Put the devices in /etc/modbus_exporter/config.yml, then:
    #   systemctl enable --now modbus_exporter.service
    #
    # On an upgrade, start the new version if the service is running.
    systemctl try-restart modbus_exporter.service 2>/dev/null || true
fi
