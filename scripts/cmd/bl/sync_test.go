package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var productionFlushFile = flushFile

func init() {
	flushFile = func(*os.File) error { return nil }
}

func TestTestBinaryUsesNoopFlush(t *testing.T) {
	if reflect.ValueOf(flushFile).Pointer() == reflect.ValueOf(productionFlushFile).Pointer() {
		t.Fatal("test binary is using the production flush")
	}
}

func TestProductionFlushFileReturnsErrorForClosedFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := productionFlushFile(f); err == nil {
		t.Fatal("production flush accepted a closed file")
	}
}

func TestLedgerAndAtomicWriteWorkWithTestFlush(t *testing.T) {
	dir := t.TempDir()
	if _, err := appendEvent(dir, Event{T: "file", ID: "A1", Text: "test"}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "view.txt")
	if err := atomicWrite(path, "view"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "view" {
		t.Fatalf("atomicWrite() = %q, want %q", got, "view")
	}
}
