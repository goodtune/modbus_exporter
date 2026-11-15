package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The exporter must not be able to write to a device. This test fails when
// a Go file of the repository names a Modbus write function of the client
// library.
func TestNoModbusWriteFunction(t *testing.T) {
	write := regexp.MustCompile(`\b(WriteSingleCoil|WriteMultipleCoils|WriteSingleRegister|WriteMultipleRegisters|MaskWriteRegister|ReadWriteMultipleRegisters)\b`)
	checked := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || path == "readonly_test.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		if match := write.Find(data); match != nil {
			t.Errorf("%s names the Modbus write function %s", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no Go file was checked")
	}
}
