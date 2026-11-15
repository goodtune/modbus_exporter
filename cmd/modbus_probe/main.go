// Command modbus_probe reads blocks of Modbus registers from a device and
// prints the raw words. Use it to find and to check the registers of a new
// device profile.
//
// It reads only: function 0x04 (input registers) and, with --holding,
// function 0x03 (holding registers). It has no write path.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/grid-x/modbus"
)

// reader is the read-only part of the Modbus client. The probe holds the
// client through this interface, so it cannot call a write function.
type reader interface {
	ReadInputRegisters(ctx context.Context, address, quantity uint16) ([]byte, error)
	ReadHoldingRegisters(ctx context.Context, address, quantity uint16) ([]byte, error)
}

func main() {
	app := kingpin.New("modbus_probe", "Read blocks of Modbus registers and print the raw words. The tool only reads.")
	var (
		address = app.Flag("address", "Address of the device or its Modbus TCP gateway, as host:port.").Required().String()
		unit    = app.Flag("unit-id", "Modbus unit id.").Default("1").Uint8()
		holding = app.Flag("holding", "Read holding registers (0x03) and not input registers (0x04).").Bool()
		pause   = app.Flag("pause", "Pause between two requests.").Default("500ms").Duration()
		timeout = app.Flag("timeout", "Timeout of one request.").Default("10s").Duration()
		blocks  = app.Arg("block", "Block to read, as START:COUNT. START is the address on the wire. COUNT is 1 to 125.").Required().Strings()
	)
	app.HelpFlag.Short('h')
	kingpin.MustParse(app.Parse(os.Args[1:]))

	handler := modbus.NewTCPClientHandler(*address)
	handler.Timeout = *timeout
	handler.SetSlave(*unit)
	ctx := context.Background()
	if err := handler.Connect(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer func() { _ = handler.Close() }()
	var client reader = modbus.NewClient(handler)

	for i, arg := range *blocks {
		if i > 0 {
			time.Sleep(*pause)
		}
		startText, countText, ok := strings.Cut(arg, ":")
		if !ok {
			countText = "1"
		}
		start, err1 := strconv.ParseUint(startText, 10, 16)
		count, err2 := strconv.ParseUint(countText, 10, 16)
		if err1 != nil || err2 != nil || count < 1 || count > 125 {
			fmt.Fprintln(os.Stderr, "bad block:", arg)
			_ = handler.Close()
			os.Exit(2)
		}
		read := client.ReadInputRegisters
		if *holding {
			read = client.ReadHoldingRegisters
		}
		begin := time.Now()
		data, err := read(ctx, uint16(start), uint16(count))
		if err != nil {
			fmt.Printf("# %s: error after %s: %v\n", arg, time.Since(begin).Round(time.Millisecond), err)
			continue
		}
		fmt.Printf("# %s: %d words in %s\n", arg, len(data)/2, time.Since(begin).Round(time.Millisecond))
		for j := 0; j+1 < len(data); j += 2 {
			w := binary.BigEndian.Uint16(data[j:])
			fmt.Printf("%5d  0x%04x  %6d  %6d  %q\n", int(start)+j/2, w, w, int16(w), string(data[j:j+2]))
		}
	}
}
