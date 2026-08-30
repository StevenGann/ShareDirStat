package api

import (
	"net/http"
	"time"

	"github.com/StevenGann/ShareDirStat/internal/model"
	"github.com/StevenGann/ShareDirStat/internal/share"
)

// generationFor resolves the share and its current results, writing the
// appropriate error response when either is missing.
func (s *Server) generationFor(w http.ResponseWriter, r *http.Request) (*share.Share, *model.Generation, bool) {
	id := r.PathValue("id")
	sh, ok := s.reg.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "share_not_found", "no share with id "+id, nil)
		return nil, nil, false
	}
	gen := sh.Generation()
	if gen == nil {
		writeError(w, http.StatusConflict, "share_not_scanned",
			"share "+id+" has no results yet; start a scan first",
			map[string]any{"state": sh.State()})
		return nil, nil, false
	}
	return sh, gen, true
}

// basisFor resolves the size basis for a request, defaulting to the share's.
func basisFor(r *http.Request, sh *share.Share) (model.Basis, bool) {
	raw := r.URL.Query().Get("basis")
	if raw == "" {
		raw = sh.Config.SizeBasis
	}
	return model.ParseBasis(raw)
}

// basisOrError resolves the basis and answers 400 when the client asked for
// one that does not exist. Silently falling back would label the response
// with a basis the numbers were not computed in.
func basisOrError(w http.ResponseWriter, r *http.Request, sh *share.Share) (model.Basis, bool) {
	basis, valid := basisFor(r, sh)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_basis", "basis must be apparent or allocated", nil)
		return basis, false
	}
	return basis, true
}

// rendererFor builds a renderer bound to this request's basis.
func (s *Server) rendererFor(b model.Basis, gen *model.Generation) renderer {
	st := gen.Stats()
	total := st.Size
	if b == model.BasisAllocated {
		total = st.Alloc
	}
	return renderer{owners: s.owners, basis: b, shareTotal: total}
}

// notModified handles conditional requests against the generation's ETag.
func notModified(w http.ResponseWriter, r *http.Request, gen *model.Generation) bool {
	etag := gen.ETag()
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	sh, gen, ok := s.generationFor(w, r)
	if !ok {
		return
	}
	basis, ok := basisOrError(w, r, sh)
	if !ok {
		return
	}
	sortField, valid := model.ParseSort(r.URL.Query().Get("sort"))
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort must be size, name, mtime or files", nil)
		return
	}
	if notModified(w, r, gen) {
		return
	}

	opts := model.ListOptions{
		Path:   queryPath(r),
		Basis:  basis,
		Sort:   sortField,
		Desc:   r.URL.Query().Get("order") != "asc",
		Offset: clampInt(r, "offset", 0, 0, 1<<30),
		Limit:  clampInt(r, "limit", DefaultListLimit, 1, MaxListLimit),
		Depth:  clampInt(r, "depth", 1, 1, MaxTreeDepth),
	}
	res, found := gen.Tree(opts)
	if !found {
		writeError(w, http.StatusNotFound, "path_not_found", "no such path in the current results: "+opts.Path, nil)
		return
	}

	rend := s.rendererFor(basis, gen)
	parentSize := rend.sized(&res.Node)
	writeJSON(w, http.StatusOK, map[string]any{
		"share":      sh.Config.ID,
		"generation": gen.ID(),
		"basis":      basis.String(),
		"node":       rend.node(res.Node, 0),
		"ancestors":  rend.nodes(res.Ancestors, 0),
		"children":   rend.treeChildren(res.Children, parentSize),
		"total":      res.Total,
		"offset":     res.Offset,
		"limit":      res.Limit,
	})
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	sh, gen, ok := s.generationFor(w, r)
	if !ok {
		return
	}
	basis, ok := basisOrError(w, r, sh)
	if !ok {
		return
	}
	if notModified(w, r, gen) {
		return
	}
	path := queryPath(r)
	info, found := gen.Info(path)
	if !found {
		writeError(w, http.StatusNotFound, "path_not_found", "no such path in the current results: "+path, nil)
		return
	}
	anc, _ := gen.Ancestors(path)
	rend := s.rendererFor(basis, gen)
	var parentSize uint64
	if len(anc) > 0 {
		p := anc[len(anc)-1]
		parentSize = rend.sized(&p)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"share":      sh.Config.ID,
		"generation": gen.ID(),
		"basis":      basis.String(),
		"node":       rend.node(info, parentSize),
		"ancestors":  rend.nodes(anc, 0),
	})
}

func (s *Server) handleTreemap(w http.ResponseWriter, r *http.Request) {
	sh, gen, ok := s.generationFor(w, r)
	if !ok {
		return
	}
	basis, ok := basisOrError(w, r, sh)
	if !ok {
		return
	}
	if notModified(w, r, gen) {
		return
	}
	opts := model.TreemapOptions{
		Path:        queryPath(r),
		Basis:       basis,
		MinFraction: clampFloat(r, "min_fraction", 0.0005, 0, 1),
		MaxNodes:    clampInt(r, "max_nodes", 10000, 1, MaxTreemapNodes),
		MaxDepth:    clampInt(r, "max_depth", 8, 1, 64),
	}
	root, emitted, found := gen.Treemap(opts)
	if !found {
		writeError(w, http.StatusNotFound, "path_not_found", "no such path in the current results: "+opts.Path, nil)
		return
	}
	rend := s.rendererFor(basis, gen)
	writeJSON(w, http.StatusOK, map[string]any{
		"share":        sh.Config.ID,
		"generation":   gen.ID(),
		"basis":        basis.String(),
		"root":         rend.treemap(root, 0),
		"nodes":        emitted,
		"min_fraction": opts.MinFraction,
		"max_depth":    opts.MaxDepth,
	})
}

func (s *Server) handleTop(w http.ResponseWriter, r *http.Request) {
	sh, gen, ok := s.generationFor(w, r)
	if !ok {
		return
	}
	basis, ok := basisOrError(w, r, sh)
	if !ok {
		return
	}
	if notModified(w, r, gen) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "file"
	}
	if kind != "file" && kind != "dir" {
		writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be file or dir", nil)
		return
	}
	path := queryPath(r)
	list, found := gen.Top(path, clampInt(r, "n", 100, 1, MaxTopN), kind == "dir", basis)
	if !found {
		writeError(w, http.StatusNotFound, "path_not_found", "no such path in the current results: "+path, nil)
		return
	}
	rend := s.rendererFor(basis, gen)
	writeJSON(w, http.StatusOK, map[string]any{
		"share": sh.Config.ID, "generation": gen.ID(), "basis": basis.String(),
		"path": path, "kind": kind, "items": rend.nodes(list, 0),
	})
}

func (s *Server) handleExtensions(w http.ResponseWriter, r *http.Request) {
	sh, gen, ok := s.generationFor(w, r)
	if !ok {
		return
	}
	if notModified(w, r, gen) {
		return
	}
	path := queryPath(r)
	exts, found := gen.ExtensionsUnder(path)
	if !found {
		writeError(w, http.StatusNotFound, "path_not_found", "no such path in the current results: "+path, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"share": sh.Config.ID, "generation": gen.ID(), "path": path, "extensions": exts,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	sh, gen, ok := s.generationFor(w, r)
	if !ok {
		return
	}
	basis, ok := basisOrError(w, r, sh)
	if !ok {
		return
	}
	q := r.URL.Query()
	opts := model.SearchOptions{
		Query:       q.Get("q"),
		Path:        queryPath(r),
		Kind:        q.Get("kind"),
		Ext:         q.Get("ext"),
		MinSize:     queryUint(r, "min_size"),
		MaxSize:     queryUint(r, "max_size"),
		MtimeBefore: queryTime(r, "mtime_before"),
		MtimeAfter:  queryTime(r, "mtime_after"),
		Basis:       basis,
		Limit:       clampInt(r, "limit", 200, 1, MaxSearchLimit),
		Deadline:    time.Now().Add(SearchTimeout),
	}
	res, found := gen.Search(opts)
	if !found {
		writeError(w, http.StatusNotFound, "path_not_found", "no such path in the current results: "+opts.Path, nil)
		return
	}
	rend := s.rendererFor(basis, gen)
	writeJSON(w, http.StatusOK, map[string]any{
		"share": sh.Config.ID, "generation": gen.ID(), "basis": basis.String(),
		"matches": rend.nodes(res.Matches, 0), "total": res.Total,
		"truncated": res.Truncated, "scanned": res.Scanned,
	})
}

func (s *Server) handleErrors(w http.ResponseWriter, r *http.Request) {
	sh, gen, ok := s.generationFor(w, r)
	if !ok {
		return
	}
	offset := clampInt(r, "offset", 0, 0, 1<<30)
	limit := clampInt(r, "limit", 200, 1, MaxListLimit)
	errs, total, dropped := gen.Errors(offset, limit)
	if errs == nil {
		errs = []model.ScanError{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"share": sh.Config.ID, "generation": gen.ID(),
		"errors": errs, "total": total, "dropped": dropped,
		"offset": offset, "limit": limit,
	})
}
