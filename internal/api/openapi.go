package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/StevenGann/ShareDirStat/docs"
)

// openAPIJSON converts the embedded YAML document to JSON once, on first
// request. The YAML stays the source of truth so the file that humans read
// and the one machines fetch cannot drift apart.
var openAPIJSON = sync.OnceValues(func() ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(docs.OpenAPI, &doc); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
})

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	body, err := openAPIJSON()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "the API document could not be rendered: "+err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}

func (s *Server) handleOpenAPIYAML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(docs.OpenAPI)
}
