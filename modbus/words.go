package modbus

import (
	"math"
	"strings"
)

// Table is a Modbus register table that the exporter can read.
type Table int

// The two tables of 16-bit registers. There is one read function for each.
const (
	Input   Table = iota // function 0x04, read input registers
	Holding              // function 0x03, read holding registers
)

func (t Table) String() string {
	if t == Holding {
		return "holding"
	}
	return "input"
}

// Kind is the data type of a register value.
type Kind int

// U16 and S16 use one register. U32, S32 and F32 use two registers.
const (
	U16 Kind = iota
	S16
	U32
	S32
	F32
)

// Words returns the number of registers that a value of the kind uses.
func (k Kind) Words() uint16 {
	if k == U16 || k == S16 {
		return 1
	}
	return 2
}

// WordOrder is the sequence of the two registers of a 32-bit value.
type WordOrder int

// The two word orders.
const (
	HighWordFirst WordOrder = iota
	LowWordFirst
)

// Key identifies one register: its table and its address on the wire.
type Key struct {
	Table Table
	Addr  uint16
}

// Words holds the register values of one poll.
type Words map[Key]uint16

// Raw returns the bits of the value at addr. The second result is false when
// a register of the value was not read.
func (w Words) Raw(table Table, addr uint16, kind Kind, order WordOrder) (uint32, bool) {
	first, ok := w[Key{table, addr}]
	if !ok {
		return 0, false
	}
	if kind.Words() == 1 {
		return uint32(first), true
	}
	second, ok := w[Key{table, addr + 1}]
	if !ok {
		return 0, false
	}
	if order == LowWordFirst {
		return uint32(second)<<16 | uint32(first), true
	}
	return uint32(first)<<16 | uint32(second), true
}

// Float changes the bits of a value into a number.
func Float(kind Kind, raw uint32) float64 {
	switch kind {
	case S16:
		return float64(int16(raw))
	case S32:
		return float64(int32(raw))
	case F32:
		return float64(math.Float32frombits(raw))
	}
	return float64(raw)
}

// Text decodes count registers from addr as text, two characters for each
// register. The text stops at the first NUL. The second result is false when
// a register of the text was not read.
func (w Words) Text(table Table, addr, count uint16) (string, bool) {
	var sb strings.Builder
	for i := uint16(0); i < count; i++ {
		word, ok := w[Key{table, addr + i}]
		if !ok {
			return "", false
		}
		for _, c := range []byte{byte(word >> 8), byte(word)} {
			if c == 0 {
				return strings.TrimSpace(sb.String()), true
			}
			sb.WriteByte(c)
		}
	}
	return strings.TrimSpace(sb.String()), true
}
