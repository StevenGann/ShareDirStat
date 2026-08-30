// Package config loads, validates and exposes the application configuration.
//
// Sources, highest precedence first: CLI flags, environment variables
// (prefix SDS_, nested keys joined with "__"), the YAML file, built-in
// defaults. See docs/SPECIFICATION.md §5.
package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the fully resolved application configuration.
type Config struct {
	Server  Server `yaml:"server"`
	DataDir string `yaml:"data_dir"`
	// SnapshotDebounce bounds how often a snapshot is rewritten after the
	// model is mutated by a delete, so a burst of deletes costs one write
	// rather than one per file (FR-DATA-04).
	SnapshotDebounce Duration   `yaml:"snapshot_debounce"`
	Log              Log        `yaml:"log"`
	Scan             Scan       `yaml:"scan"`
	Operations       Operations `yaml:"operations"`
	Discovery        Discovery  `yaml:"discovery"`
	Shares           []Share    `yaml:"shares"`
	UI               UI         `yaml:"ui"`
}

// Server holds HTTP listener settings.
type Server struct {
	Listen            string   `yaml:"listen"`
	BasePath          string   `yaml:"base_path"`
	AllowedHosts      []string `yaml:"allowed_hosts"`
	TrustProxyHeaders bool     `yaml:"trust_proxy_headers"`
	ReadTimeout       Duration `yaml:"read_timeout"`
	ShutdownTimeout   Duration `yaml:"shutdown_timeout"`
}

// Log holds logging settings.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Scan holds crawler defaults that individual shares may override.
type Scan struct {
	OnStartup           string   `yaml:"on_startup"`
	MaxConcurrentShares int      `yaml:"max_concurrent_shares"`
	DefaultConcurrency  int      `yaml:"default_concurrency"`
	DefaultSchedule     string   `yaml:"default_schedule"`
	FollowSymlinks      bool     `yaml:"follow_symlinks"`
	CrossMountPoints    bool     `yaml:"cross_mount_points"`
	DefaultExcludes     []string `yaml:"default_excludes"`
	SizeBasis           string   `yaml:"size_basis"`
	MaxNodesPerShare    int      `yaml:"max_nodes_per_share"`
}

// Operations holds delete/download settings.
type Operations struct {
	Readonly bool        `yaml:"readonly"`
	Delete   DeleteOps   `yaml:"delete"`
	Download DownloadOps `yaml:"download"`
}

// DeleteOps configures deletion behaviour.
type DeleteOps struct {
	ConfirmMode string `yaml:"confirm_mode"`
	Trash       Trash  `yaml:"trash"`
}

// Trash configures trash mode (FR-DEL-07).
type Trash struct {
	Enabled   bool     `yaml:"enabled"`
	Retention Duration `yaml:"retention"`
}

// DownloadOps configures download behaviour.
type DownloadOps struct {
	ZipEnabled    bool     `yaml:"zip_enabled"`
	ZipMaxBytes   ByteSize `yaml:"zip_max_bytes"`
	ZipMaxEntries int      `yaml:"zip_max_entries"`
}

// Discovery configures automatic share discovery (FR-CFG-01).
type Discovery struct {
	// Enabled is tri-state: nil means "only when no shares are configured".
	Enabled       *bool  `yaml:"enabled"`
	Root          string `yaml:"root"`
	AllowDelete   bool   `yaml:"allow_delete"`
	AllowDownload bool   `yaml:"allow_download"`
}

// Share is one configured storage location (§6).
type Share struct {
	ID            string   `yaml:"id"`
	Name          string   `yaml:"name"`
	Path          string   `yaml:"path"`
	AllowDelete   *bool    `yaml:"allow_delete"`
	AllowDownload *bool    `yaml:"allow_download"`
	Concurrency   int      `yaml:"concurrency"`
	Schedule      *string  `yaml:"schedule"`
	Excludes      []string `yaml:"excludes"`
	SizeBasis     string   `yaml:"size_basis"`

	// Discovered is true when the share came from auto-discovery.
	Discovered bool `yaml:"-"`
}

// CanDelete reports whether deletion is permitted for the share. Pointers
// are always resolved by Load; the nil checks only guard hand-built values.
func (s Share) CanDelete() bool { return s.AllowDelete != nil && *s.AllowDelete }

// CanDownload reports whether download is permitted for the share.
func (s Share) CanDownload() bool { return s.AllowDownload != nil && *s.AllowDownload }

// CronSchedule returns the effective cron expression ("" = none).
func (s Share) CronSchedule() string {
	if s.Schedule == nil {
		return ""
	}
	return *s.Schedule
}

// UI holds defaults for the web interface.
type UI struct {
	DefaultShare string         `yaml:"default_share"`
	Columns      []string       `yaml:"columns"`
	OwnerNames   map[int]string `yaml:"owner_names"`
	GroupNames   map[int]string `yaml:"group_names"`
	Treemap      TreemapUI      `yaml:"treemap"`
}

// TreemapUI holds treemap rendering defaults.
type TreemapUI struct {
	ColorScheme string `yaml:"color_scheme"`
	Cushion     bool   `yaml:"cushion"`
}

// Duration is a time.Duration that (un)marshals as a Go duration string.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = v
	return nil
}

// ParseDuration parses a Go duration string ("30s", "1h30m", "168h").
func ParseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return Duration(v), nil
}

// ByteSize is a byte count that accepts "12MB", "1.5GiB", "4096" (FR-CFG-05).
type ByteSize int64

func (b ByteSize) String() string { return strconv.FormatInt(int64(b), 10) }

// MarshalYAML implements yaml.Marshaler.
func (b ByteSize) MarshalYAML() (any, error) { return int64(b), nil }

// UnmarshalYAML implements yaml.Unmarshaler.
func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*b = v
	return nil
}

var byteUnits = map[string]float64{
	"":    1,
	"b":   1,
	"k":   1e3,
	"kb":  1e3,
	"m":   1e6,
	"mb":  1e6,
	"g":   1e9,
	"gb":  1e9,
	"t":   1e12,
	"tb":  1e12,
	"ki":  1 << 10,
	"kib": 1 << 10,
	"mi":  1 << 20,
	"mib": 1 << 20,
	"gi":  1 << 30,
	"gib": 1 << 30,
	"ti":  1 << 40,
	"tib": 1 << 40,
}

// ParseByteSize parses a size with optional decimal or binary unit suffix.
func ParseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	i := len(s)
	for i > 0 && (s[i-1] < '0' || s[i-1] > '9') && s[i-1] != '.' {
		i--
	}
	num, unit := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
	mult, ok := byteUnits[unit]
	if !ok {
		return 0, fmt.Errorf("invalid size %q: unknown unit %q", s, unit)
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	v := f * mult
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("size %q overflows", s)
	}
	return ByteSize(int64(math.Round(v))), nil
}
