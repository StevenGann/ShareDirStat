package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// EnvPrefix is the prefix for environment overrides, e.g. SDS_SERVER__LISTEN.
const EnvPrefix = "SDS_"

// DefaultPath is where the configuration file is looked for when --config is not given.
const DefaultPath = "/config/config.yaml"

// Overrides carries CLI flag values that take precedence over every other source.
// Empty strings mean "not set".
type Overrides struct {
	Listen    string
	BasePath  string
	DataDir   string
	LogLevel  string
	LogFormat string
}

// Warning is a non-fatal issue discovered during loading, for the caller to log.
type Warning string

var (
	shareIDRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	cronParser   = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	validColumns = map[string]bool{"name": true, "size": true, "pct": true, "alloc": true, "files": true, "dirs": true, "mtime": true, "owner": true, "perms": true, "ext": true, "items": true, "duration": true, "spm": true}
)

// Default returns the built-in defaults (§5.2).
func Default() *Config {
	return &Config{
		Server: Server{
			Listen:          ":8080",
			BasePath:        "/",
			ReadTimeout:     Duration(30 * time.Second),
			ShutdownTimeout: Duration(20 * time.Second),
		},
		DataDir:          "/data",
		SnapshotDebounce: Duration(30 * time.Second),
		Log:              Log{Level: "info", Format: "json"},
		Scan: Scan{
			OnStartup:           "if-missing",
			MaxConcurrentShares: 2,
			DefaultConcurrency:  4,
			DefaultSchedule:     "0 3 * * *",
			DefaultExcludes: []string{
				// Keep in step with ops.TrashDir, which this package cannot
				// import without a cycle. FR-DEL-07 requires the trash to be
				// excluded from scans: without it, a trashed folder is
				// re-indexed on the next scan and the space it "freed" comes
				// straight back, and the trash becomes selectable for delete.
				"**/.sharedirstat-trash",
				"**/.snapshot", "**/@eaDir", "**/#recycle", "**/.Trash-*", "**/lost+found",
			},
			SizeBasis:        "apparent",
			MaxNodesPerShare: 20_000_000,
			MediaDurations:   true,
		},
		Operations: Operations{
			Delete:   DeleteOps{ConfirmMode: "name", Trash: Trash{Retention: Duration(168 * time.Hour)}},
			Download: DownloadOps{ZipEnabled: true},
		},
		Discovery: Discovery{Root: "/shares", AllowDelete: false, AllowDownload: true},
		UI: UI{
			Columns: []string{"name", "size", "pct", "files", "dirs", "mtime", "owner"},
			Treemap: TreemapUI{ColorScheme: "extension", Cushion: true},
		},
	}
}

// Load builds the effective configuration from file (optional), environment,
// and overrides, runs auto-discovery and validates the result.
//
// path == "" means DefaultPath, which is allowed to be absent. An explicit
// path that does not exist is an error.
func Load(path string, ov Overrides) (*Config, []Warning, error) {
	cfg := Default()
	var warnings []Warning

	explicit := path != ""
	if !explicit {
		path = DefaultPath
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := unmarshalStrict(data, cfg); err != nil {
			return nil, nil, fmt.Errorf("config file %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist) && !explicit:
		warnings = append(warnings, Warning(fmt.Sprintf("no config file at %s; using defaults, environment and auto-discovery", path)))
	default:
		return nil, nil, fmt.Errorf("config file %s: %w", path, err)
	}

	if err := applyEnv(reflect.ValueOf(cfg).Elem(), EnvPrefix, os.LookupEnv); err != nil {
		return nil, nil, err
	}
	applyOverrides(cfg, ov)

	w, err := discover(cfg)
	warnings = append(warnings, w...)
	if err != nil {
		return nil, nil, err
	}

	normalize(cfg)
	if err := Validate(cfg); err != nil {
		return nil, nil, err
	}
	if len(cfg.Shares) == 0 {
		warnings = append(warnings, Warning("no shares configured: add a 'shares:' list or mount directories under "+cfg.Discovery.Root))
	}
	if len(cfg.Server.AllowedHosts) == 0 {
		warnings = append(warnings, Warning("server.allowed_hosts is empty; set it to defend against DNS rebinding (§12.2)"))
	}
	return cfg, warnings, nil
}

func unmarshalStrict(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err.Error() == "EOF" { // empty file
			return nil
		}
		return err
	}
	return nil
}

func applyOverrides(cfg *Config, ov Overrides) {
	if ov.Listen != "" {
		cfg.Server.Listen = ov.Listen
	}
	if ov.BasePath != "" {
		cfg.Server.BasePath = ov.BasePath
	}
	if ov.DataDir != "" {
		cfg.DataDir = ov.DataDir
	}
	if ov.LogLevel != "" {
		cfg.Log.Level = ov.LogLevel
	}
	if ov.LogFormat != "" {
		cfg.Log.Format = ov.LogFormat
	}
}

var (
	durationType = reflect.TypeOf(Duration(0))
	byteSizeType = reflect.TypeOf(ByteSize(0))
)

// applyEnv walks the struct and applies SDS_* overrides to scalar and
// []string fields. Lists of structs (shares) and maps cannot be set via
// environment; use the config file for those.
func applyEnv(v reflect.Value, prefix string, lookup func(string) (string, bool)) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		key := prefix + strings.ToUpper(tag)
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct && f.Type != durationType {
			if err := applyEnv(fv, key+"__", lookup); err != nil {
				return err
			}
			continue
		}
		raw, ok := lookup(key)
		if !ok {
			continue
		}
		if err := setFromString(fv, raw); err != nil {
			return fmt.Errorf("environment %s=%q: %w", key, raw, err)
		}
	}
	return nil
}

func setFromString(fv reflect.Value, raw string) error {
	switch fv.Type() {
	case durationType:
		d, err := ParseDuration(raw)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(d))
		return nil
	case byteSizeType:
		b, err := ParseByteSize(raw)
		if err != nil {
			return err
		}
		fv.Set(reflect.ValueOf(b))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("cannot set %s from environment", fv.Type())
		}
		var out []string
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		fv.Set(reflect.ValueOf(out))
	case reflect.Pointer:
		elem := reflect.New(fv.Type().Elem())
		if err := setFromString(elem.Elem(), raw); err != nil {
			return err
		}
		fv.Set(elem)
	case reflect.Map:
		return fmt.Errorf("cannot set %s from environment; use the config file", fv.Type())
	default:
		return fmt.Errorf("unsupported type %s", fv.Type())
	}
	return nil
}

// discover adds a share for every immediate subdirectory of Discovery.Root
// when discovery is enabled (FR-CFG-01, FR-CFG-06).
func discover(cfg *Config) ([]Warning, error) {
	d := cfg.Discovery
	enabled := len(cfg.Shares) == 0
	if d.Enabled != nil {
		enabled = *d.Enabled
	}
	if !enabled {
		return nil, nil
	}
	entries, err := os.ReadDir(d.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && d.Enabled == nil {
			return []Warning{Warning(fmt.Sprintf("auto-discovery root %s does not exist", d.Root))}, nil
		}
		return nil, fmt.Errorf("discovery.root %s: %w", d.Root, err)
	}
	existing := map[string]bool{}
	for _, s := range cfg.Shares {
		existing[s.ID] = true
	}
	var warnings []Warning
	seen := map[string]string{}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// DirEntry.IsDir reads d_type, which is DT_LNK for a symlink to a
		// directory, so `ln -s /mnt/tank/media /shares/media` -- a very common
		// layout -- would be skipped silently. Stat through the link instead.
		if !e.IsDir() {
			fi, err := os.Stat(filepath.Join(d.Root, e.Name()))
			if err != nil || !fi.IsDir() {
				continue
			}
		}
		names = append(names, e.Name())
	}
	sort.Slice(names, func(i, j int) bool { return sanitizeID(names[i]) < sanitizeID(names[j]) })
	for _, name := range names {
		id := sanitizeID(name)
		if id == "" {
			warnings = append(warnings, Warning(fmt.Sprintf("discovery: skipping %q (cannot derive a valid share id)", name)))
			continue
		}
		if existing[id] {
			continue // explicit configuration wins on id collision
		}
		if prev, dup := seen[id]; dup {
			return nil, fmt.Errorf("discovery: directories %q and %q both map to share id %q; configure them explicitly", prev, name, id)
		}
		seen[id] = name
		allowDelete, allowDownload := d.AllowDelete, d.AllowDownload
		cfg.Shares = append(cfg.Shares, Share{
			ID:            id,
			Name:          name,
			Path:          filepath.Join(d.Root, name),
			AllowDelete:   &allowDelete,
			AllowDownload: &allowDownload,
			Discovered:    true,
		})
	}
	return warnings, nil
}

func sanitizeID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	id := strings.Trim(b.String(), "-_")
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

// normalize resolves per-share defaults and canonicalises paths so that
// downstream code never has to consult scan-level defaults.
func normalize(cfg *Config) {
	cfg.Server.BasePath = "/" + strings.Trim(cfg.Server.BasePath, "/")
	cfg.DataDir = filepath.Clean(cfg.DataDir)
	cfg.Discovery.Root = filepath.Clean(cfg.Discovery.Root)
	for i := range cfg.Shares {
		s := &cfg.Shares[i]
		s.ID = strings.TrimSpace(s.ID)
		if s.Name == "" {
			s.Name = s.ID
		}
		if s.Path != "" {
			s.Path = filepath.Clean(s.Path)
		}
		if s.AllowDelete == nil {
			v := false
			s.AllowDelete = &v
		}
		if s.AllowDownload == nil {
			v := true
			s.AllowDownload = &v
		}
		if s.Concurrency == 0 {
			s.Concurrency = cfg.Scan.DefaultConcurrency
		}
		if s.Schedule == nil {
			v := cfg.Scan.DefaultSchedule
			s.Schedule = &v
		}
		if s.SizeBasis == "" {
			s.SizeBasis = cfg.Scan.SizeBasis
		}
		if s.MediaDurations == nil {
			v := cfg.Scan.MediaDurations
			s.MediaDurations = &v
		}
		s.Excludes = append(append([]string{}, cfg.Scan.DefaultExcludes...), s.Excludes...)
	}
}

// Validate checks the configuration (FR-CFG-02). It returns the first error found.
func Validate(cfg *Config) error {
	var errs []error
	check := func(cond bool, format string, args ...any) {
		if !cond {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	oneOf := func(field, val string, allowed ...string) {
		for _, a := range allowed {
			if val == a {
				return
			}
		}
		errs = append(errs, fmt.Errorf("%s: %q is not one of %s", field, val, strings.Join(allowed, "|")))
	}

	check(cfg.Server.Listen != "", "server.listen: must not be empty")
	check(strings.HasPrefix(cfg.Server.BasePath, "/"), "server.base_path: must start with /")
	check(cfg.Server.ReadTimeout > 0, "server.read_timeout: must be > 0")
	check(cfg.Server.ShutdownTimeout > 0, "server.shutdown_timeout: must be > 0")
	check(filepath.IsAbs(cfg.DataDir), "data_dir: %q must be an absolute path", cfg.DataDir)
	check(cfg.SnapshotDebounce >= 0, "snapshot_debounce: must not be negative")
	oneOf("log.level", cfg.Log.Level, "debug", "info", "warn", "error")
	oneOf("log.format", cfg.Log.Format, "json", "text")
	oneOf("scan.on_startup", cfg.Scan.OnStartup, "never", "if-missing", "always")
	check(cfg.Scan.MaxConcurrentShares >= 1, "scan.max_concurrent_shares: must be >= 1")
	check(cfg.Scan.DefaultConcurrency >= 1, "scan.default_concurrency: must be >= 1")
	oneOf("scan.size_basis", cfg.Scan.SizeBasis, "apparent", "allocated")
	check(cfg.Scan.MaxNodesPerShare >= 1000, "scan.max_nodes_per_share: must be >= 1000")
	if cfg.Scan.DefaultSchedule != "" {
		if _, err := cronParser.Parse(cfg.Scan.DefaultSchedule); err != nil {
			errs = append(errs, fmt.Errorf("scan.default_schedule: %q: %w", cfg.Scan.DefaultSchedule, err))
		}
	}
	oneOf("operations.delete.confirm_mode", cfg.Operations.Delete.ConfirmMode, "simple", "name")
	check(cfg.Operations.Download.ZipMaxBytes >= 0, "operations.download.zip_max_bytes: must be >= 0")
	check(cfg.Operations.Download.ZipMaxEntries >= 0, "operations.download.zip_max_entries: must be >= 0")
	check(filepath.IsAbs(cfg.Discovery.Root), "discovery.root: %q must be an absolute path", cfg.Discovery.Root)
	oneOf("ui.treemap.color_scheme", cfg.UI.Treemap.ColorScheme, "extension", "depth", "mtime")
	for _, c := range cfg.UI.Columns {
		check(validColumns[c], "ui.columns: unknown column %q", c)
	}

	ids := map[string]int{}
	for i, s := range cfg.Shares {
		where := fmt.Sprintf("shares[%d]", i)
		if s.ID != "" {
			where = fmt.Sprintf("shares[%d] (%s)", i, s.ID)
		}
		check(shareIDRe.MatchString(s.ID), "%s: id %q must match %s", where, s.ID, shareIDRe)
		if j, dup := ids[s.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate id (also shares[%d])", where, j))
		}
		ids[s.ID] = i
		check(s.Path != "" && filepath.IsAbs(s.Path), "%s: path %q must be an absolute path", where, s.Path)
		check(s.Path != "/", "%s: path must not be the filesystem root", where)
		check(s.Concurrency >= 1, "%s: concurrency must be >= 1", where)
		oneOf(where+".size_basis", s.SizeBasis, "apparent", "allocated")
		if sched := s.CronSchedule(); sched != "" {
			if _, err := cronParser.Parse(sched); err != nil {
				errs = append(errs, fmt.Errorf("%s: schedule %q: %w", where, sched, err))
			}
		}
		for j, o := range cfg.Shares {
			if i == j || o.Path == "" || s.Path == "" {
				continue
			}
			if o.Path == s.Path && i > j {
				errs = append(errs, fmt.Errorf("%s: path %s is already used by share %q (FR-SHR-03)", where, s.Path, o.ID))
			}
			if isSubPath(o.Path, s.Path) {
				errs = append(errs, fmt.Errorf("%s: path %s is nested inside share %q (%s) (FR-SHR-03)", where, s.Path, o.ID, o.Path))
			}
		}
	}
	if cfg.UI.DefaultShare != "" {
		_, ok := ids[cfg.UI.DefaultShare]
		check(ok, "ui.default_share: no share with id %q", cfg.UI.DefaultShare)
	}
	return errors.Join(errs...)
}

// isSubPath reports whether child is strictly inside parent.
func isSubPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, "../")
}

// ShareByID returns the share with the given id.
func (c *Config) ShareByID(id string) (Share, bool) {
	for _, s := range c.Shares {
		if s.ID == id {
			return s, true
		}
	}
	return Share{}, false
}

// Redacted returns the effective configuration as a generic map suitable for
// JSON output (FR-CFG-03). There are no secrets today; this is the hook
// where they would be masked.
func (c *Config) Redacted() (map[string]any, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
