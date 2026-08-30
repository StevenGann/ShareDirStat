// Package docs embeds the documents the server publishes about itself.
package docs

import _ "embed"

// OpenAPI is the API description served at /api/v1/openapi.json and .yaml.
//
//go:embed openapi.yaml
var OpenAPI []byte
