package modbus

import "testing"

func TestRawAndFloat(t *testing.T) {
	w := Words{
		{Input, 10}: 0x9F5B, {Input, 11}: 0x0005, // U32 with the low word first
		{Input, 20}: 0xFFCA, {Input, 21}: 0xFFFF, // S32 -54
		{Input, 30}: 0xFF85,                      // S16 -123
		{Input, 40}: 0x4248, {Input, 41}: 0x0000, // F32 50.0 with the high word first
		{Holding, 10}: 7,
	}
	tests := []struct {
		table Table
		addr  uint16
		kind  Kind
		order WordOrder
		want  float64
		ok    bool
	}{
		{Input, 10, U32, LowWordFirst, 5*65536 + 0x9F5B, true},
		{Input, 10, U32, HighWordFirst, 0x9F5B*65536 + 5, true},
		{Input, 20, S32, LowWordFirst, -54, true},
		{Input, 30, S16, LowWordFirst, -123, true},
		{Input, 30, U16, LowWordFirst, 0xFF85, true},
		{Input, 40, F32, HighWordFirst, 50, true},
		{Holding, 10, U16, HighWordFirst, 7, true},
		{Holding, 20, U16, HighWordFirst, 0, false}, // the other table
		{Input, 11, U32, LowWordFirst, 0, false},    // the second word was not read
		{Input, 99, S16, LowWordFirst, 0, false},
	}
	for _, tt := range tests {
		raw, ok := w.Raw(tt.table, tt.addr, tt.kind, tt.order)
		if ok != tt.ok {
			t.Errorf("Raw(%v, %d, %v) ok = %v, want %v", tt.table, tt.addr, tt.kind, ok, tt.ok)
			continue
		}
		if got := Float(tt.kind, raw); ok && got != tt.want {
			t.Errorf("Raw(%v, %d, %v) = %v, want %v", tt.table, tt.addr, tt.kind, got, tt.want)
		}
	}
}

func TestText(t *testing.T) {
	w := Words{
		{Input, 100}: 'S'<<8 | 'H', {Input, 101}: '1'<<8 | '5', {Input, 102}: 'T' << 8, {Input, 103}: 'X'<<8 | 'X',
	}
	if got, ok := w.Text(Input, 100, 4); !ok || got != "SH15T" {
		t.Errorf("Text = %q, %v; want SH15T, true", got, ok)
	}
	if _, ok := w.Text(Input, 103, 2); ok {
		t.Error("Text reported a value for registers that were not read")
	}
}

func TestKindWords(t *testing.T) {
	for kind, want := range map[Kind]uint16{U16: 1, S16: 1, U32: 2, S32: 2, F32: 2} {
		if got := kind.Words(); got != want {
			t.Errorf("Kind(%d).Words() = %d, want %d", kind, got, want)
		}
	}
}
