package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/events"
	"github.com/StevenGann/ShareDirStat/internal/metrics"
	"github.com/StevenGann/ShareDirStat/internal/ops"
	"github.com/StevenGann/ShareDirStat/internal/scan"
	"github.com/StevenGann/ShareDirStat/internal/share"
	"github.com/StevenGann/ShareDirStat/internal/snapshot"
)

type testEnv struct {
	srv       *Server
	scans     *scan.Manager
	ops       *ops.Manager
	reg       *share.Registry
	sharePath string
	dataDir   string
}

// newEnv builds a server backed by a real on-disk share.
func newEnv(t *testing.T, mutate func(*config.Config), ui fstest.MapFS) *testEnv {
	t.Helper()
	base := t.TempDir()
	sharePath := filepath.Join(base, "media")
	for _, d := range []string{"Movies/2019", "Music"} {
		if err := os.MkdirAll(filepath.Join(sharePath, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string, n int) {
		if err := os.WriteFile(filepath.Join(sharePath, p), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Movies/2019/big.mkv", 8000)
	write("Movies/2019/small.mkv", 100)
	write("Movies/trailer.mp4", 2000)
	write("Music/song.flac", 500)
	write("readme.txt", 42)

	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.DataDir = dataDir
	cfg.Scan.OnStartup = "never"
	cfg.Scan.DefaultSchedule = ""
	tr := true
	sched := ""
	cfg.Shares = []config.Share{{
		ID: "media", Name: "Media", Path: sharePath,
		AllowDelete: &tr, AllowDownload: &tr, Concurrency: 4,
		Schedule: &sched, SizeBasis: "apparent",
	}}
	if mutate != nil {
		mutate(cfg)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := share.New(cfg, log)
	reg.CheckAll()
	store, err := snapshot.NewStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBroker()
	scans := scan.NewManager(cfg, reg, store, bus, log, scan.Hooks{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		scans.Close(ctx)
	})
	fileOps, err := ops.NewManager(cfg, reg, bus, log, ops.Hooks{
		RunningScanPath: func(shareID string) (string, bool) {
			st, running := scans.RunningFor(shareID)
			return st.Path, running
		},
		MarkDirty: scans.MarkDirty,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fileOps.Close() })

	if ui == nil {
		ui = fstest.MapFS{}
	}
	srv := New(Deps{
		Config: cfg, Shares: reg, Scans: scans, Ops: fileOps, Events: bus,
		Metrics: metrics.New(), Log: log, UI: ui,
		Owners: NewOwnerResolver(map[int]string{0: "root"}, map[int]string{0: "root"}),
	})
	return &testEnv{srv: srv, scans: scans, ops: fileOps, reg: reg, sharePath: sharePath, dataDir: dataDir}
}

// scanNow runs a full scan synchronously.
func (e *testEnv) scanNow(t *testing.T) {
	t.Helper()
	if _, err := e.scans.Start(context.Background(), "media", "", scan.TriggerManual); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sh, _ := e.reg.Get("media")
		if sh.Generation() != nil {
			if _, running := e.scans.RunningFor("media"); !running {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("scan did not finish in time")
}

func do(t *testing.T, h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

func TestHealthAndReady(t *testing.T) {
	e := newEnv(t, nil, nil)
	if rec := do(t, e.srv.Handler(), "GET", "/healthz", nil); rec.Code != 200 {
		t.Errorf("healthz = %d", rec.Code)
	}
	if rec := do(t, e.srv.Handler(), "GET", "/readyz", nil); rec.Code != 503 {
		t.Errorf("readyz before ready = %d", rec.Code)
	}
	e.srv.SetReady(true)
	if rec := do(t, e.srv.Handler(), "GET", "/readyz", nil); rec.Code != 200 {
		t.Errorf("readyz after ready = %d", rec.Code)
	}
}

func TestSharesEndpoint(t *testing.T) {
	e := newEnv(t, nil, nil)
	rec := do(t, e.srv.Handler(), "GET", "/api/v1/shares", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := decode[struct {
		Shares []shareView `json:"shares"`
	}](t, rec)
	if len(body.Shares) != 1 || body.Shares[0].ID != "media" || body.Shares[0].State != share.StateNeverScanned {
		t.Errorf("shares = %+v", body.Shares)
	}
	if rec := do(t, e.srv.Handler(), "GET", "/api/v1/shares/nope", nil); rec.Code != 404 {
		t.Errorf("unknown share: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, e.srv.Handler(), "GET", "/api/v1/whatever", nil); rec.Code != 404 ||
		!strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Errorf("unknown api route should be JSON 404: %d %s", rec.Code, rec.Body)
	}
}

func TestQueriesBeforeScanAreRejectedClearly(t *testing.T) {
	e := newEnv(t, nil, nil)
	for _, p := range []string{"tree", "treemap", "top", "extensions", "search", "node"} {
		rec := do(t, e.srv.Handler(), "GET", "/api/v1/shares/media/"+p, nil)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "share_not_scanned") {
			t.Errorf("%s before scan = %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestScanThenBrowse(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	t.Run("share reports results", func(t *testing.T) {
		v := decode[shareView](t, do(t, h, "GET", "/api/v1/shares/media", nil))
		if v.State != share.StateReady || v.Generation == nil || v.Stats == nil {
			t.Fatalf("share = %+v", v)
		}
		if v.Stats.Size != 8000+100+2000+500+42 {
			t.Errorf("total size = %d", v.Stats.Size)
		}
		if v.Stats.Files != 5 || v.Stats.Dirs != 3 {
			t.Errorf("files=%d dirs=%d", v.Stats.Files, v.Stats.Dirs)
		}
	})

	t.Run("tree", func(t *testing.T) {
		rec := do(t, h, "GET", "/api/v1/shares/media/tree", nil)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		res := decode[struct {
			Children []nodeJSON `json:"children"`
			Total    int        `json:"total"`
			Node     nodeJSON   `json:"node"`
		}](t, rec)
		if res.Total != 3 {
			t.Errorf("root children = %d, want 3", res.Total)
		}
		if res.Children[0].Name != "Movies" {
			t.Errorf("largest first: got %q", res.Children[0].Name)
		}
		if res.Children[0].PctOfParent <= 0.9 {
			t.Errorf("pct_of_parent = %v", res.Children[0].PctOfParent)
		}
		if res.Children[0].Perms == "" || !strings.HasPrefix(res.Children[0].Perms, "d") {
			t.Errorf("perms = %q", res.Children[0].Perms)
		}
		if res.Children[0].Owner == "" {
			t.Error("owner name not resolved")
		}
		if etag := rec.Header().Get("ETag"); etag == "" {
			t.Error("missing ETag")
		}
	})

	t.Run("etag revalidation", func(t *testing.T) {
		first := do(t, h, "GET", "/api/v1/shares/media/tree", nil)
		etag := first.Header().Get("ETag")
		second := do(t, h, "GET", "/api/v1/shares/media/tree", map[string]string{"If-None-Match": etag})
		if second.Code != http.StatusNotModified {
			t.Errorf("conditional GET = %d, want 304", second.Code)
		}
	})

	t.Run("tree paging and sorting", func(t *testing.T) {
		res := decode[struct {
			Children []nodeJSON `json:"children"`
			Total    int        `json:"total"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/tree?limit=1&offset=1", nil))
		if len(res.Children) != 1 || res.Total != 3 {
			t.Errorf("paging = %+v", res)
		}
		byName := decode[struct {
			Children []nodeJSON `json:"children"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/tree?sort=name&order=asc", nil))
		if byName.Children[0].Name != "Movies" || byName.Children[2].Name != "readme.txt" {
			t.Errorf("name order = %+v", byName.Children)
		}
		if rec := do(t, h, "GET", "/api/v1/shares/media/tree?sort=bogus", nil); rec.Code != 400 {
			t.Errorf("bad sort = %d", rec.Code)
		}
		if rec := do(t, h, "GET", "/api/v1/shares/media/tree?basis=nonsense", nil); rec.Code != 400 {
			t.Errorf("bad basis = %d", rec.Code)
		}
		if rec := do(t, h, "GET", "/api/v1/shares/media/tree?path=nope", nil); rec.Code != 404 {
			t.Errorf("missing path = %d", rec.Code)
		}
	})

	t.Run("nested subtree", func(t *testing.T) {
		res := decode[struct {
			Node     nodeJSON `json:"node"`
			Children []nodeJSON
		}](t, do(t, h, "GET", "/api/v1/shares/media/tree?path=Movies/2019", nil))
		if res.Node.Path != "Movies/2019" || res.Node.Size != 8100 {
			t.Errorf("node = %+v", res.Node)
		}
	})

	t.Run("treemap conserves area", func(t *testing.T) {
		res := decode[struct {
			Root  nodeJSON `json:"root"`
			Nodes int      `json:"nodes"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/treemap?max_nodes=100", nil))
		if res.Nodes < 5 {
			t.Errorf("emitted %d nodes", res.Nodes)
		}
		var sum uint64
		for _, c := range res.Root.SubChildren {
			sum += c.Size
		}
		if res.Root.Truncated != nil {
			sum += res.Root.Truncated.Size
		}
		if sum != res.Root.Size {
			t.Errorf("area not conserved: children %d vs root %d", sum, res.Root.Size)
		}
	})

	t.Run("top files", func(t *testing.T) {
		res := decode[struct {
			Items []nodeJSON `json:"items"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/top?n=2", nil))
		if len(res.Items) != 2 || res.Items[0].Name != "big.mkv" {
			t.Errorf("top = %+v", res.Items)
		}
		if rec := do(t, h, "GET", "/api/v1/shares/media/top?kind=bogus", nil); rec.Code != 400 {
			t.Errorf("bad kind = %d", rec.Code)
		}
	})

	t.Run("extensions", func(t *testing.T) {
		res := decode[struct {
			Extensions []struct {
				Ext   string `json:"ext"`
				Files uint64 `json:"files"`
				Size  uint64 `json:"size"`
			} `json:"extensions"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/extensions", nil))
		if len(res.Extensions) != 4 || res.Extensions[0].Ext != "mkv" || res.Extensions[0].Files != 2 {
			t.Errorf("extensions = %+v", res.Extensions)
		}
	})

	t.Run("search", func(t *testing.T) {
		res := decode[struct {
			Matches   []nodeJSON `json:"matches"`
			Total     int        `json:"total"`
			Truncated bool       `json:"truncated"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/search?q=mkv", nil))
		if res.Total != 2 || res.Matches[0].Name != "big.mkv" {
			t.Errorf("search = %+v", res)
		}
		sized := decode[struct {
			Total int `json:"total"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/search?min_size=1000&kind=file", nil))
		if sized.Total != 2 {
			t.Errorf("size-filtered search found %d, want 2", sized.Total)
		}
	})

	t.Run("errors list", func(t *testing.T) {
		res := decode[struct {
			Total int `json:"total"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/errors", nil))
		if res.Total != 0 {
			t.Errorf("clean tree reported %d errors", res.Total)
		}
	})

	t.Run("scan history", func(t *testing.T) {
		res := decode[struct {
			Scans []scan.Record `json:"scans"`
		}](t, do(t, h, "GET", "/api/v1/shares/media/scans", nil))
		if len(res.Scans) != 1 || res.Scans[0].Outcome != scan.OutcomeCompleted {
			t.Errorf("history = %+v", res.Scans)
		}
		if res.Scans[0].Files != 5 {
			t.Errorf("history files = %d", res.Scans[0].Files)
		}
	})
}

func TestScanEndpointsRequireCSRFAndReportConflicts(t *testing.T) {
	e := newEnv(t, nil, nil)
	h := e.srv.Handler()
	csrf := map[string]string{"X-Requested-With": CSRFHeaderValue}

	if rec := do(t, h, "POST", "/api/v1/shares/media/scan", nil); rec.Code != 403 {
		t.Errorf("scan without CSRF header = %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/v1/shares/nope/scan", csrf); rec.Code != 404 {
		t.Errorf("scan of unknown share = %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, "POST", "/api/v1/shares/media/scan", csrf)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("scan start = %d %s", rec.Code, rec.Body)
	}
	st := decode[scan.Status](t, rec)
	if st.ID == "" || st.ShareID != "media" {
		t.Errorf("status = %+v", st)
	}
	if rec := do(t, h, "GET", "/api/v1/scans", nil); rec.Code != 200 {
		t.Errorf("running scans = %d", rec.Code)
	}
	// Cancelling a scan that has already finished is a 404, not a crash.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, running := e.scans.RunningFor("media"); !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec := do(t, h, "POST", "/api/v1/shares/media/scan/"+st.ID+"/cancel", csrf); rec.Code != 404 {
		t.Errorf("cancel of a finished scan = %d %s", rec.Code, rec.Body)
	}
}

func TestSubtreeRescanEndpoint(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	if err := os.WriteFile(filepath.Join(e.sharePath, "Music/new.flac"), make([]byte, 1500), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/shares/media/scan", strings.NewReader(`{"path":"Music"}`))
	req.Header.Set("X-Requested-With", CSRFHeaderValue)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("subtree rescan = %d %s", rec.Code, rec.Body)
	}
	if st := decode[scan.Status](t, rec); st.Trigger != scan.TriggerRescan || st.Path != "Music" {
		t.Errorf("status = %+v", st)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, running := e.scans.RunningFor("media"); !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	res := decode[struct {
		Node nodeJSON `json:"node"`
	}](t, do(t, h, "GET", "/api/v1/shares/media/node?path=Music", nil))
	if res.Node.Size != 2000 {
		t.Errorf("Music after rescan = %d, want 2000", res.Node.Size)
	}
	total := decode[shareView](t, do(t, h, "GET", "/api/v1/shares/media", nil))
	if total.Stats.Size != 8000+100+2000+500+42+1500 {
		t.Errorf("share total after rescan = %d", total.Stats.Size)
	}
	// An unrelated subtree must be untouched by the splice.
	movies := decode[struct {
		Node nodeJSON `json:"node"`
	}](t, do(t, h, "GET", "/api/v1/shares/media/node?path=Movies", nil))
	if movies.Node.Size != 10100 {
		t.Errorf("Movies damaged by splice: %d", movies.Node.Size)
	}
}

func TestResultsSurviveRestart(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	before := decode[shareView](t, do(t, e.srv.Handler(), "GET", "/api/v1/shares/media", nil))

	// A second process against the same data directory must come up with the
	// previous results already loaded.
	e2 := newEnv(t, func(c *config.Config) {
		c.DataDir = e.dataDir
		c.Shares[0].Path = e.sharePath
	}, nil)
	e2.scans.LoadSnapshots()
	after := decode[shareView](t, do(t, e2.srv.Handler(), "GET", "/api/v1/shares/media", nil))

	if after.State != share.StateReady {
		t.Fatalf("state after restart = %s", after.State)
	}
	if after.Generation == nil || *after.Generation != *before.Generation {
		t.Errorf("generation changed across restart")
	}
	if after.Stats.Size != before.Stats.Size || after.Stats.Files != before.Stats.Files {
		t.Errorf("stats changed across restart: %+v vs %+v", after.Stats, before.Stats)
	}
	res := decode[struct {
		Children []nodeJSON `json:"children"`
	}](t, do(t, e2.srv.Handler(), "GET", "/api/v1/shares/media/tree?path=Movies/2019", nil))
	if len(res.Children) != 2 || res.Children[0].Name != "big.mkv" {
		t.Errorf("tree after restart = %+v", res.Children)
	}
}

func TestSSEStream(t *testing.T) {
	e := newEnv(t, nil, nil)
	srv := httptest.NewServer(e.srv.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}

	if _, err := e.scans.Start(context.Background(), "media", "", scan.TriggerManual); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && len(seen) < 2 {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			seen[strings.TrimPrefix(line, "event: ")] = true
		}
		if seen["scan.completed"] {
			break
		}
	}
	if !seen["scan.started"] {
		t.Errorf("did not observe scan.started; saw %v", seen)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t, nil, nil)
	rec := do(t, e.srv.Handler(), "GET", "/api/v1/shares", nil)
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Error("missing CSP")
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID")
	}
}

func TestHostCheck(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Server.AllowedHosts = []string{"sds.lan", "*.home.arpa"}
	}, nil)
	cases := map[string]int{
		"sds.lan": 200, "sds.lan:8080": 200, "pi.home.arpa": 200,
		"home.arpa": 200, "evil.com": 421, "127.0.0.1": 421,
	}
	for host, want := range cases {
		req := httptest.NewRequest("GET", "/api/v1/shares", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		e.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q: got %d want %d", host, rec.Code, want)
		}
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t, nil, nil)
	h := e.srv.Handler()
	csrf := map[string]string{"X-Requested-With": CSRFHeaderValue}

	if rec := do(t, h, "POST", "/api/v1/shares/media/scan", nil); rec.Code != 403 {
		t.Errorf("no header: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/v1/x", map[string]string{
		"X-Requested-With": CSRFHeaderValue, "Sec-Fetch-Site": "cross-site",
	}); rec.Code != 403 {
		t.Errorf("cross-site: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/v1/x", map[string]string{
		"X-Requested-With": CSRFHeaderValue, "Origin": "http://evil.example",
	}); rec.Code != 403 {
		t.Errorf("bad origin: %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/api/v1/x", map[string]string{
		"X-Requested-With": CSRFHeaderValue, "Origin": "http://example.com",
	}); rec.Code != 404 {
		t.Errorf("matching origin should pass CSRF: %d", rec.Code)
	}
	if rec := do(t, h, "OPTIONS", "/api/v1/x", map[string]string{
		"Origin": "http://evil.example", "Access-Control-Request-Method": "POST",
	}); rec.Code != 403 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight must be refused without CORS headers: %d %v", rec.Code, rec.Header())
	}
	if rec := do(t, h, "GET", "/api/v1/shares", csrf); rec.Code != 200 {
		t.Errorf("GET should never need CSRF: %d", rec.Code)
	}
}

func TestBasePath(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Server.BasePath = "/sds" }, nil)
	h := e.srv.Handler()
	if rec := do(t, h, "GET", "/sds/api/v1/shares", nil); rec.Code != 200 {
		t.Errorf("prefixed api: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/api/v1/shares", nil); rec.Code != 404 {
		t.Errorf("unprefixed should 404: %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/sds", nil); rec.Code != 301 || rec.Header().Get("Location") != "/sds/" {
		t.Errorf("bare prefix should redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(t, h, "GET", "/sds/healthz", nil); rec.Code != 200 {
		t.Errorf("prefixed healthz: %d", rec.Code)
	}
}

func TestSPA(t *testing.T) {
	e := newEnv(t, nil, nil)
	if rec := do(t, e.srv.Handler(), "GET", "/", nil); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), "not built") {
		t.Errorf("placeholder: %d %s", rec.Code, rec.Body)
	}
	ui := fstest.MapFS{
		"index.html":        {Data: []byte("<html>app</html>")},
		"assets/app.abc.js": {Data: []byte("console.log(1)")},
	}
	e2 := newEnv(t, nil, ui)
	h := e2.srv.Handler()
	if rec := do(t, h, "GET", "/", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("index: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/assets/app.abc.js", nil); rec.Code != 200 ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := do(t, h, "GET", "/some/deep/route", nil); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), "app") {
		t.Errorf("fallback: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/metrics", nil); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), "sharedirstat_build_info") {
		t.Errorf("metrics: %d", rec.Code)
	}
}

// TestOpenAPIDocument checks the embedded document parses, is served in both
// forms, and describes every route the router actually has. A route added
// without documentation fails here (FR-API-01).
func TestOpenAPIDocument(t *testing.T) {
	e := newEnv(t, nil, nil)
	h := e.srv.Handler()

	rec := do(t, h, "GET", "/api/v1/openapi.json", nil)
	if rec.Code != 200 {
		t.Fatalf("openapi.json = %d %s", rec.Code, rec.Body)
	}
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the served document is not JSON: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi version = %q", doc.OpenAPI)
	}
	if rec := do(t, h, "GET", "/api/v1/openapi.yaml", nil); rec.Code != 200 ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/yaml") {
		t.Errorf("openapi.yaml = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// Every API route the server registers must be documented. The list is
	// maintained by hand so that adding a route forces a documentation
	// decision rather than a silent gap.
	routes := map[string][]string{
		"/shares":                           {"get"},
		"/shares/{id}":                      {"get"},
		"/shares/{id}/tree":                 {"get"},
		"/shares/{id}/node":                 {"get"},
		"/shares/{id}/treemap":              {"get"},
		"/shares/{id}/top":                  {"get"},
		"/shares/{id}/extensions":           {"get"},
		"/shares/{id}/search":               {"get"},
		"/shares/{id}/errors":               {"get"},
		"/shares/{id}/scans":                {"get"},
		"/shares/{id}/scan":                 {"post"},
		"/shares/{id}/scan/{scanId}/cancel": {"post"},
		"/shares/{id}/scan/{scanId}/pause":  {"post"},
		"/shares/{id}/scan/{scanId}/resume": {"post"},
		"/scans":                            {"get"},
		"/shares/{id}/download":             {"get"},
		"/shares/{id}/download/zip":         {"get", "post"},
		"/shares/{id}/delete/preview":       {"post"},
		"/shares/{id}/delete":               {"post"},
		"/shares/{id}/trash":                {"get"},
		"/shares/{id}/trash/restore":        {"post"},
		"/shares/{id}/trash/empty":          {"post"},
		"/audit/deletes":                    {"get"},
		"/events":                           {"get"},
		"/version":                          {"get"},
		"/config":                           {"get"},
	}
	for path, methods := range routes {
		ops, ok := doc.Paths[path]
		if !ok {
			t.Errorf("route %s is served but not documented in docs/openapi.yaml", path)
			continue
		}
		for _, m := range methods {
			if _, ok := ops[m]; !ok {
				t.Errorf("route %s %s is served but not documented", strings.ToUpper(m), path)
			}
		}
	}
	for path := range doc.Paths {
		if _, ok := routes[path]; !ok {
			t.Errorf("docs/openapi.yaml documents %s, which the server does not serve", path)
		}
	}
}
