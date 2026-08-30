package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "sharedirstat") {
		t.Fatalf("version: code=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown command should exit 2, got %d", code)
	}
}

func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()
	share := filepath.Join(dir, "s")
	_ = os.Mkdir(share, 0o755)
	cfg := filepath.Join(dir, "c.yaml")
	_ = os.WriteFile(cfg, []byte("shares:\n  - id: s\n    path: "+share+"\n"), 0o644)

	var out, errOut bytes.Buffer
	if code := run([]string{"check-config", "--config", cfg, "--listen", ":1234"}, &out, &errOut); code != 0 {
		t.Fatalf("check-config failed: %d %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "listen: :1234") {
		t.Errorf("effective config should show override:\n%s", out.String())
	}

	_ = os.WriteFile(cfg, []byte("shares:\n  - id: BAD\n    path: rel\n"), 0o644)
	if code := run([]string{"check-config", "--config", cfg}, &out, &errOut); code != 1 {
		t.Fatalf("invalid config should exit 1, got %d", code)
	}
}
