package modbus

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeServer is a small Modbus TCP server. Each register holds its own
// address. A read at address 9000 or more gets the exception "illegal data
// address". The server records the function code of each request.
type fakeServer struct {
	listener net.Listener

	mu          sync.Mutex
	functions   []byte
	connections int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.connections++
			s.mu.Unlock()
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		header := make([]byte, 7)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		pdu := make([]byte, binary.BigEndian.Uint16(header[4:6])-1)
		if _, err := io.ReadFull(conn, pdu); err != nil {
			return
		}
		s.mu.Lock()
		s.functions = append(s.functions, pdu[0])
		s.mu.Unlock()

		var reply []byte
		start := binary.BigEndian.Uint16(pdu[1:3])
		count := binary.BigEndian.Uint16(pdu[3:5])
		switch {
		case pdu[0] != 0x03 && pdu[0] != 0x04:
			reply = []byte{pdu[0] | 0x80, 0x01} // illegal function
		case start >= 9000:
			reply = []byte{pdu[0] | 0x80, 0x02} // illegal data address
		default:
			reply = []byte{pdu[0], byte(2 * count)}
			for i := uint16(0); i < count; i++ {
				reply = binary.BigEndian.AppendUint16(reply, start+i)
			}
		}
		binary.BigEndian.PutUint16(header[4:6], uint16(len(reply)+1))
		if _, err := conn.Write(append(header, reply...)); err != nil {
			return
		}
	}
}

func TestClientRead(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.listener.Addr().String(), 1, 2*time.Second, time.Millisecond)

	blocks := []Block{
		{Name: "first", Table: Input, Start: 100, Count: 3},
		{Name: "missing", Table: Input, Start: 9000, Count: 2},
		{Name: "settings", Table: Holding, Start: 200, Count: 2},
	}
	for poll := 0; poll < 2; poll++ {
		result, err := client.Read(context.Background(), blocks)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if len(result.Words) != 5 {
			t.Errorf("got %d words, want 5", len(result.Words))
		}
		for _, key := range []Key{{Input, 100}, {Input, 102}, {Holding, 200}, {Holding, 201}} {
			if got, ok := result.Words[key]; !ok || got != key.Addr {
				t.Errorf("word %v = %d, %v; want %d", key, got, ok, key.Addr)
			}
		}
		if len(result.Failed) != 1 || result.Failed["missing"] == nil {
			t.Errorf("Failed = %v, want only the block %q", result.Failed, "missing")
		}
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if server.connections != 2 {
		t.Errorf("the client opened %d connections for 2 polls, want 2", server.connections)
	}
	want := []byte{0x04, 0x04, 0x03, 0x04, 0x04, 0x03}
	if string(server.functions) != string(want) {
		t.Errorf("function codes = %v, want %v", server.functions, want)
	}
}

func TestClientReadConnectError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	client := NewClient(address, 1, time.Second, time.Millisecond)
	if _, err := client.Read(context.Background(), []Block{{Name: "first", Start: 0, Count: 1}}); err == nil {
		t.Error("Read to a closed port returned no error")
	}
}
