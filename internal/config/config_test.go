package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsAreValid(t *testing.T) {
	cfg := Default()
	normalize(cfg)
	if err := Validate(cfg); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestLoadMissingDefaultFileIsAllowed(t *testing.T) {
	t.Setenv("SDS_DISCOVERY__ROOT", filepath.Join(t.TempDir(), "nope"))
	cfg, warnings, err := Load("", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":8080" {
		t.Errorf("listen = %q", cfg.Server.Listen)
	}
	if len(warnings) == 0 {
		t.Error("expected warnings about missing file / discovery root")
	}
}

func TestLoadExplicitMissingFileFails(t *testing.T) {
	if _, _, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), Overrides{}); err == nil {
		t.Fatal("expected error for explicit missing file")
	}
}

func TestLoadFileAndEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	share := filepath.Join(dir, "media")
	_ = os.Mkdir(share, 0o755)
	p := writeFile(t, dir, "config.yaml", `
server:
  listen: ":9000"
  base_path: /sds/
  read_timeout: 45s
scan:
  default_concurrency: 8
operations:
  download:
    zip_max_bytes: 1.5GiB
shares:
  - id: media
    name: Media
    path: `+share+`
    allow_delete: true
    schedule: "@daily"
`)
	t.Setenv("SDS_SERVER__LISTEN", ":9100")
	t.Setenv("SDS_SCAN__DEFAULT_EXCLUDES", "**/a, **/b")
	t.Setenv("SDS_OPERATIONS__READONLY", "true")
	t.Setenv("SDS_SERVER__SHUTDOWN_TIMEOUT", "5s")

	cfg, _, err := Load(p, Overrides{DataDir: "/tmp/sds-data"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":9100" {
		t.Errorf("env should beat file: listen=%q", cfg.Server.Listen)
	}
	if cfg.Server.BasePath != "/sds" {
		t.Errorf("base_path normalised = %q", cfg.Server.BasePath)
	}
	if cfg.Server.ReadTimeout.D() != 45*time.Second || cfg.Server.ShutdownTimeout.D() != 5*time.Second {
		t.Errorf("durations: %v %v", cfg.Server.ReadTimeout, cfg.Server.ShutdownTimeout)
	}
	if cfg.DataDir != "/tmp/sds-data" {
		t.Errorf("override should beat everything: data_dir=%q", cfg.DataDir)
	}
	if !cfg.Operations.Readonly {
		t.Error("readonly env override not applied")
	}
	if got := cfg.Scan.DefaultExcludes; !reflect.DeepEqual(got, []string{"**/a", "**/b"}) {
		t.Errorf("excludes = %v", got)
	}
	if cfg.Operations.Download.ZipMaxBytes != ByteSize(1.5*(1<<30)) {
		t.Errorf("zip_max_bytes = %d", cfg.Operations.Download.ZipMaxBytes)
	}
	if len(cfg.Shares) != 1 {
		t.Fatalf("shares = %+v", cfg.Shares)
	}
	s := cfg.Shares[0]
	if !s.CanDelete() || !s.CanDownload() || s.Concurrency != 8 || s.CronSchedule() != "@daily" || s.SizeBasis != "apparent" {
		t.Errorf("share defaults not resolved: %+v", s)
	}
	if !reflect.DeepEqual(s.Excludes, []string{"**/a", "**/b"}) {
		t.Errorf("share excludes should inherit defaults: %v", s.Excludes)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	p := writeFile(t, t.TempDir(), "c.yaml", "server:\n  port: 1\n")
	_, _, err := Load(p, Overrides{})
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("expected unknown-field error naming 'port', got %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"bad id":        "shares:\n  - id: Bad_ID!\n    path: /x\n",
		"dup id":        "shares:\n  - id: a\n    path: /x\n  - id: a\n    path: /y\n",
		"nested":        "shares:\n  - id: a\n    path: /x\n  - id: b\n    path: /x/y\n",
		"relative":      "shares:\n  - id: a\n    path: x\n",
		"root":          "shares:\n  - id: a\n    path: /\n",
		"bad cron":      "shares:\n  - id: a\n    path: /x\n    schedule: 'every day'\n",
		"bad level":     "log:\n  level: loud\n",
		"bad basis":     "scan:\n  size_basis: real\n",
		"bad column":    "ui:\n  columns: [name, colour]\n",
		"default share": "shares:\n  - id: a\n    path: /x\nui:\n  default_share: b\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "c.yaml", body)
			if _, _, err := Load(p, Overrides{}); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestDiscovery(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Media Files", "backups", ".hidden", "photos"} {
		_ = os.Mkdir(filepath.Join(root, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(root, "notadir"), nil, 0o644)

	p := writeFile(t, t.TempDir(), "c.yaml", "discovery:\n  root: "+root+"\n  allow_delete: true\n")
	cfg, _, err := Load(p, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range cfg.Shares {
		ids = append(ids, s.ID)
		if !s.Discovered || !s.CanDelete() {
			t.Errorf("share %s: discovered=%v delete=%v", s.ID, s.Discovered, s.CanDelete())
		}
	}
	if !reflect.DeepEqual(ids, []string{"backups", "media-files", "photos"}) {
		t.Errorf("ids = %v", ids)
	}

	// Explicit shares disable discovery unless discovery.enabled is true.
	p2 := writeFile(t, t.TempDir(), "c.yaml", "discovery:\n  root: "+root+"\nshares:\n  - id: x\n    path: /x\n")
	cfg2, _, err := Load(p2, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Shares) != 1 {
		t.Errorf("expected discovery off with explicit shares, got %d shares", len(cfg2.Shares))
	}

	p3 := writeFile(t, t.TempDir(), "c.yaml", "discovery:\n  root: "+root+"\n  enabled: true\nshares:\n  - id: photos\n    name: Explicit\n    path: /elsewhere\n")
	cfg3, _, err := Load(p3, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg3.Shares) != 3 {
		t.Errorf("expected explicit + 2 discovered, got %d", len(cfg3.Shares))
	}
	if s, _ := cfg3.ShareByID("photos"); s.Path != "/elsewhere" || s.Discovered {
		t.Errorf("explicit share must win on collision: %+v", s)
	}
}

func TestParseByteSize(t *testing.T) {
	cases := map[string]ByteSize{
		"0": 0, "4096": 4096, "12MB": 12_000_000, "1.5GiB": 1610612736, "2 tb": 2_000_000_000_000, "512KiB": 524288,
	}
	for in, want := range cases {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1", "12 parsecs", "abc"} {
		if _, err := ParseByteSize(bad); err == nil {
			t.Errorf("ParseByteSize(%q) should fail", bad)
		}
	}
}

func TestRedacted(t *testing.T) {
	cfg := Default()
	normalize(cfg)
	m, err := cfg.Redacted()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["server"]; !ok {
		t.Errorf("redacted config missing server section: %v", m)
	}
}
