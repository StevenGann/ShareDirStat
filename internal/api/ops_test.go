package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StevenGann/ShareDirStat/internal/config"
	"github.com/StevenGann/ShareDirStat/internal/ops"
)

// post issues a mutating request with the CSRF header the API requires.
func post(t *testing.T, h http.Handler, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, target, r)
	req.Header.Set("X-Requested-With", CSRFHeaderValue)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "api-test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeleteFlow(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	// The share advertises that deleting is available.
	view := decode[shareView](t, do(t, h, "GET", "/api/v1/shares/media", nil))
	if view.DeleteBlocked != "" {
		t.Fatalf("delete should be available: %q", view.DeleteBlocked)
	}

	prev := post(t, h, "/api/v1/shares/media/delete/preview", map[string]any{
		"paths": []string{"Movies/2019/big.mkv"},
	})
	if prev.Code != 200 {
		t.Fatalf("preview = %d %s", prev.Code, prev.Body)
	}
	p := decode[ops.Preview](t, prev)
	if len(p.Targets) != 1 || p.Targets[0].Size != 8000 || p.TotalSize != 8000 {
		t.Fatalf("preview = %+v", p)
	}
	if p.Confirm == "" {
		t.Fatal("no confirmation token")
	}
	// A single file needs no typed confirmation.
	if p.NameToType != "" {
		t.Errorf("name_to_type = %q, want empty for one file", p.NameToType)
	}

	del := post(t, h, "/api/v1/shares/media/delete", map[string]any{
		"paths": []string{"Movies/2019/big.mkv"}, "confirm": p.Confirm,
	})
	if del.Code != 200 {
		t.Fatalf("delete = %d %s", del.Code, del.Body)
	}
	res := decode[ops.DeleteResult](t, del)
	if res.FreedTotal != 8000 || res.Results[0].Outcome != ops.OutcomeDeleted {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(e.sharePath, "Movies/2019/big.mkv")); !os.IsNotExist(err) {
		t.Error("the file is still on disk")
	}

	// The tree reflects it immediately, with no rescan.
	tree := decode[struct {
		Node     nodeJSON   `json:"node"`
		Children []nodeJSON `json:"children"`
	}](t, do(t, h, "GET", "/api/v1/shares/media/tree?path=Movies/2019", nil))
	if tree.Node.Size != 100 || len(tree.Children) != 1 {
		t.Errorf("tree after delete = %+v", tree)
	}

	// And the audit log records it.
	audit := decode[struct {
		Deletions []ops.AuditEntry `json:"deletions"`
	}](t, do(t, h, "GET", "/api/v1/audit/deletes", nil))
	if len(audit.Deletions) != 1 || audit.Deletions[0].Path != "Movies/2019/big.mkv" ||
		audit.Deletions[0].UserAgent != "api-test" {
		t.Errorf("audit = %+v", audit.Deletions)
	}
}

// A delete updates the in-memory model, but the snapshot on disk still
// describes the old tree. Without a rewrite a restart would resurrect
// everything just deleted (FR-DATA-04).
func TestDeleteSurvivesRestart(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.SnapshotDebounce = 0 // write through, so the test does not sleep
	}, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	p := decode[ops.Preview](t, post(t, h, "/api/v1/shares/media/delete/preview", map[string]any{
		"paths": []string{"Movies/2019/big.mkv"},
	}))
	if rec := post(t, h, "/api/v1/shares/media/delete", map[string]any{
		"paths": []string{"Movies/2019/big.mkv"}, "confirm": p.Confirm,
	}); rec.Code != 200 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	before := decode[shareView](t, do(t, h, "GET", "/api/v1/shares/media", nil))

	// A second process against the same data directory must not see the file.
	e2 := newEnv(t, func(c *config.Config) {
		c.DataDir = e.dataDir
		c.Shares[0].Path = e.sharePath
	}, nil)
	e2.scans.LoadSnapshots()
	h2 := e2.srv.Handler()

	after := decode[shareView](t, do(t, h2, "GET", "/api/v1/shares/media", nil))
	if after.Stats == nil {
		t.Fatal("no results after restart")
	}
	if after.Stats.Size != before.Stats.Size {
		t.Errorf("size after restart = %d, want %d (the delete was lost)", after.Stats.Size, before.Stats.Size)
	}
	rec := do(t, h2, "GET", "/api/v1/shares/media/node?path=Movies/2019/big.mkv", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("the deleted file came back after a restart: %d %s", rec.Code, rec.Body)
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	// No token at all.
	rec := post(t, h, "/api/v1/shares/media/delete", map[string]any{
		"paths": []string{"readme.txt"},
	})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "confirmation_required") {
		t.Errorf("unconfirmed delete = %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(e.sharePath, "readme.txt")); err != nil {
		t.Fatal("the file must survive an unconfirmed delete")
	}

	// A token for a different path.
	p := decode[ops.Preview](t, post(t, h, "/api/v1/shares/media/delete/preview", map[string]any{
		"paths": []string{"Music/song.flac"},
	}))
	rec = post(t, h, "/api/v1/shares/media/delete", map[string]any{
		"paths": []string{"readme.txt"}, "confirm": p.Confirm,
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("mismatched token = %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(e.sharePath, "readme.txt")); err != nil {
		t.Fatal("the wrong file was deleted")
	}
}

func TestDeleteWithoutCSRFHeader(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shares/media/delete",
		strings.NewReader(`{"paths":["readme.txt"],"confirm":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "csrf_rejected") {
		t.Errorf("delete without the CSRF header = %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(e.sharePath, "readme.txt")); err != nil {
		t.Fatal("SECURITY: a cross-origin delete succeeded")
	}
}

func TestDeleteRejectsTraversal(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()
	for _, p := range []string{"../../etc/passwd", "", "a/../../x"} {
		rec := post(t, h, "/api/v1/shares/media/delete/preview", map[string]any{"paths": []string{p}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("preview of %q = %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestDeleteDisabledShare(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { f := false; c.Shares[0].AllowDelete = &f }, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	view := decode[shareView](t, do(t, h, "GET", "/api/v1/shares/media", nil))
	if view.DeleteBlocked == "" {
		t.Error("the share should report why deleting is unavailable")
	}
	rec := post(t, h, "/api/v1/shares/media/delete/preview", map[string]any{"paths": []string{"readme.txt"}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "delete_disabled") {
		t.Errorf("preview on a read-only share = %d %s", rec.Code, rec.Body)
	}
}

func TestDownloadEndpoint(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	rec := do(t, h, "GET", "/api/v1/shares/media/download?path=Movies/2019/big.mkv", nil)
	if rec.Code != 200 {
		t.Fatalf("download = %d %s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 8000 {
		t.Errorf("body = %d bytes, want 8000", rec.Body.Len())
	}
	hdr := rec.Header()
	if !strings.Contains(hdr.Get("Content-Disposition"), `filename="big.mkv"`) {
		t.Errorf("Content-Disposition = %q", hdr.Get("Content-Disposition"))
	}
	if hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("downloads must be nosniff")
	}
	if hdr.Get("Accept-Ranges") != "bytes" {
		t.Error("downloads should advertise range support")
	}
	if hdr.Get("ETag") == "" {
		t.Error("missing ETag")
	}
}

func TestDownloadRangeRequest(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	rec := do(t, e.srv.Handler(), "GET", "/api/v1/shares/media/download?path=Movies/2019/big.mkv",
		map[string]string{"Range": "bytes=100-199"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range request = %d, want 206", rec.Code)
	}
	if rec.Body.Len() != 100 {
		t.Errorf("range body = %d bytes, want 100", rec.Body.Len())
	}
	if cr := rec.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 100-199/8000") {
		t.Errorf("Content-Range = %q", cr)
	}
}

func TestDownloadRefusals(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()
	cases := map[string]int{
		"path=Movies":           http.StatusBadRequest, // a directory
		"path=":                 http.StatusBadRequest, // the share root
		"path=../../etc/passwd": http.StatusBadRequest,
		"path=nope.bin":         http.StatusNotFound,
	}
	for q, want := range cases {
		rec := do(t, h, "GET", "/api/v1/shares/media/download?"+q, nil)
		if rec.Code != want {
			t.Errorf("download?%s = %d, want %d (%s)", q, rec.Code, want, rec.Body)
		}
	}

	off := newEnv(t, func(c *config.Config) { f := false; c.Shares[0].AllowDownload = &f }, nil)
	off.scanNow(t)
	rec := do(t, off.srv.Handler(), "GET", "/api/v1/shares/media/download?path=readme.txt", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("disabled download = %d %s", rec.Code, rec.Body)
	}
}

func TestDownloadNeverServesActiveContent(t *testing.T) {
	e := newEnv(t, nil, nil)
	// A hostile HTML file on the share must not come back as text/html, or a
	// browser would run it in the app's own origin (FR-SEC-04).
	if err := os.WriteFile(filepath.Join(e.sharePath, "evil.html"),
		[]byte("<script>alert(1)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.sharePath, "evil.svg"),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	e.scanNow(t)
	h := e.srv.Handler()

	for _, name := range []string{"evil.html", "evil.svg"} {
		rec := do(t, h, "GET", "/api/v1/shares/media/download?path="+name, nil)
		if rec.Code != 200 {
			t.Fatalf("%s = %d", name, rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if ct != "application/octet-stream" {
			t.Errorf("SECURITY: %s served as %q", name, ct)
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Errorf("SECURITY: %s was not sent as an attachment", name)
		}
	}
}

func TestZipDownload(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	rec := do(t, h, "GET", "/api/v1/shares/media/download/zip?path=Movies", nil)
	if rec.Code != 200 {
		t.Fatalf("zip = %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "Movies.zip") {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}

	data := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the archive is not readable: %v", err)
	}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Method != zip.Store {
			t.Errorf("%s uses method %d, want Store", f.Name, f.Method)
		}
	}
	if len(names) != 3 {
		t.Errorf("entries = %v", names)
	}
}

func TestZipMultiSelect(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.scanNow(t)
	h := e.srv.Handler()

	entries := func(rec *httptest.ResponseRecorder) []string {
		t.Helper()
		if rec.Code != 200 {
			t.Fatalf("zip = %d %s", rec.Code, rec.Body)
		}
		data := rec.Body.Bytes()
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(zr.File))
		for _, f := range zr.File {
			names = append(names, f.Name)
		}
		return names
	}

	// POST with a JSON body, for API clients.
	byPost := entries(post(t, h, "/api/v1/shares/media/download/zip", map[string]any{
		"paths": []string{"Music/song.flac", "readme.txt"},
	}))
	if len(byPost) != 2 {
		t.Errorf("POST entries = %v", byPost)
	}

	// GET with repeated path parameters, which is what the browser uses.
	byGet := entries(do(t, h,
		"GET", "/api/v1/shares/media/download/zip?path=Music/song.flac&path=readme.txt", nil))
	if len(byGet) != 2 {
		t.Errorf("GET entries = %v", byGet)
	}

	// No paths at all is a clean error, not an empty archive.
	if rec := do(t, h, "GET", "/api/v1/shares/media/download/zip", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("zip without paths = %d %s", rec.Code, rec.Body)
	}
}

func TestZipTooLarge(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Operations.Download.ZipMaxBytes = 100 }, nil)
	e.scanNow(t)
	rec := do(t, e.srv.Handler(), "GET", "/api/v1/shares/media/download/zip?path=Movies", nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized zip = %d %s", rec.Code, rec.Body)
	}
}

func TestContentDispositionHandlesAwkwardNames(t *testing.T) {
	cases := map[string][]string{
		"movie.mkv":  {`filename="movie.mkv"`, "filename*=UTF-8''movie.mkv"},
		"café.mkv":   {`filename="caf_.mkv"`, "filename*=UTF-8''caf%C3%A9.mkv"},
		`quo"te.txt`: {`filename="quo_te.txt"`},
		"a\nb.txt":   {`filename="a_b.txt"`},
	}
	for name, wants := range cases {
		got := contentDisposition(name)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("contentDisposition(%q) = %q, want it to contain %q", name, got, want)
			}
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("SECURITY: header injection in %q -> %q", name, got)
		}
	}
}

func TestContentTypeDowngradesRiskyTypes(t *testing.T) {
	risky := []string{"a.html", "a.htm", "a.svg", "a.js", "a.xml", "a.xhtml"}
	for _, n := range risky {
		if got := contentType(n); got != "application/octet-stream" {
			t.Errorf("contentType(%q) = %q, want application/octet-stream", n, got)
		}
	}
	if got := contentType("movie.mp4"); !strings.HasPrefix(got, "video/") {
		t.Errorf("contentType(mp4) = %q", got)
	}
	if got := contentType("noext"); got != "application/octet-stream" {
		t.Errorf("contentType(noext) = %q", got)
	}
}
