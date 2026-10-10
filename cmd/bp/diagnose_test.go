package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/seanheiney/keelmux/internal/diagnostics"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnoseMissingStateNeverStartsOrCreates(t *testing.T) {
	d, e := os.MkdirTemp("/tmp", "km-diag-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(d)
	marker := filepath.Join(d, "launched")
	for _, name := range []string{"keelmuxd", "bpd", "codex", "claude", "ergo"} {
		if e = os.WriteFile(filepath.Join(d, name), []byte("#!/bin/sh\necho unexpected > '"+marker+"'\n"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", d)
	state := filepath.Join(d, "absent")
	var out bytes.Buffer
	if e = diagnoseTo(context.Background(), &out, state, "", []string{"--json"}); e != nil {
		t.Fatal(e)
	}
	var report diagnostics.Report
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Version != 1 {
		t.Fatal(out.String())
	}
	if _, e = os.Stat(state); !os.IsNotExist(e) {
		t.Fatal("created state")
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("launched executable")
	}
	out.Reset()
	if e = diagnoseTo(context.Background(), &out, state, "", nil); e != nil || !strings.Contains(out.String(), "state_directory: missing") {
		t.Fatal(e, out.String())
	}
}

func TestDiagnoseHelpAndArgumentsNoStateIO(t *testing.T) {
	var out bytes.Buffer
	if e := diagnoseTo(context.Background(), &out, strings.Repeat("x", 512), "", []string{"--help"}); e != nil || !strings.Contains(out.String(), "--probe-versions") {
		t.Fatal(e, out.String())
	}
	if e := diagnoseTo(context.Background(), &out, "/tmp/should-not-exist-diagnose", "", []string{"repair"}); e == nil {
		t.Fatal("accepted repair")
	}
}
