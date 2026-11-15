// Package modbus reads registers from a device with Modbus TCP.
//
// The package only reads. It uses function 0x04 (read input registers) and
// function 0x03 (read holding registers). It has no code that calls a Modbus
// write function: a write can change how a device operates.
package modbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/grid-x/modbus"
)

// registerReader is the only part of the Modbus library that this package
// uses. The library client is held through this interface, so a write
// function cannot be called by accident.
type registerReader interface {
	ReadInputRegisters(ctx context.Context, address, quantity uint16) ([]byte, error)
	ReadHoldingRegisters(ctx context.Context, address, quantity uint16) ([]byte, error)
}

// MaxBlockCount is the largest number of registers in one read request.
const MaxBlockCount = 125

// Block is a range of registers that is read with one request. Start is the
// address on the wire.
type Block struct {
	Name  string
	Table Table
	Start uint16
	Count uint16
}

// Result is the outcome of one poll.
type Result struct {
	Words Words
	// Failed has the error of each block that could not be read.
	Failed map[string]error
}

// Client reads blocks of registers from one device. It opens one connection
// for each poll and closes it after the poll, because many devices accept
// only one Modbus TCP client at a time.
type Client struct {
	address string
	unitID  byte
	timeout time.Duration
	pause   time.Duration

	mu sync.Mutex
}

// NewClient makes a client for the device at address (host:port).
// pause is the time between two requests on the same connection.
func NewClient(address string, unitID byte, timeout, pause time.Duration) *Client {
	return &Client{address: address, unitID: unitID, timeout: timeout, pause: pause}
}

// Read reads the blocks one after the other on one connection.
//
// A Modbus exception (for example "illegal data address") fails only that
// block. All other errors stop the poll: the device is then possibly busy
// with another client, and more requests do not help.
func (c *Client) Read(ctx context.Context, blocks []Block) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	handler := modbus.NewTCPClientHandler(c.address)
	handler.Timeout = c.timeout
	handler.SetSlave(c.unitID)
	if err := handler.Connect(ctx); err != nil {
		return Result{}, fmt.Errorf("connect to %s: %w", c.address, err)
	}
	defer func() { _ = handler.Close() }()
	var client registerReader = modbus.NewClient(handler)

	result := Result{Words: Words{}, Failed: map[string]error{}}
	for i, b := range blocks {
		if i > 0 {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(c.pause):
			}
		}
		read := client.ReadInputRegisters
		if b.Table == Holding {
			read = client.ReadHoldingRegisters
		}
		data, err := read(ctx, b.Start, b.Count)
		if err != nil {
			var exception *modbus.Error
			if errors.As(err, &exception) {
				result.Failed[b.Name] = err
				continue
			}
			return result, fmt.Errorf("read block %s (%s %d, %d words): %w", b.Name, b.Table, b.Start, b.Count, err)
		}
		if len(data) != int(b.Count)*2 {
			result.Failed[b.Name] = fmt.Errorf("got %d bytes, want %d", len(data), int(b.Count)*2)
			continue
		}
		for j := uint16(0); j < b.Count; j++ {
			result.Words[Key{b.Table, b.Start + j}] = uint16(data[2*j])<<8 | uint16(data[2*j+1])
		}
	}
	return result, nil
}
