package api

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush lets SSE and streaming downloads flush through the wrapper.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request", "error", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// requestLog assigns a request id and emits one structured log line per request.
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
			return // probes are noise
		}
		s.log.Info("http",
			"id", id, "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "dur_ms", time.Since(start).Milliseconds(), "remote", clientIP(r))
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// basePath strips server.base_path and redirects the bare prefix to prefix/.
func (s *Server) basePath(next http.Handler) http.Handler {
	prefix := s.basePathPrefix()
	if prefix == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix {
			http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			http.NotFound(w, r)
			return
		}
		http.StripPrefix(prefix, next).ServeHTTP(w, r)
	})
}

// instrument records Prometheus request metrics keyed by matched route pattern.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw, ok := w.(*statusWriter)
		if !ok {
			sw = &statusWriter{ResponseWriter: w}
		}
		start := time.Now()
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		s.metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(sw.status)).Inc()
		s.metrics.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

// securityHeaders applies FR-SEC-04.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", csp)
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// hostCheck implements FR-SEC-01 (DNS rebinding defence).
func (s *Server) hostCheck(next http.Handler) http.Handler {
	allowed := s.cfg.Server.AllowedHosts
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if !hostAllowed(host, allowed) {
			writeError(w, http.StatusMisdirectedRequest, "host_not_allowed",
				"Host "+strconv.Quote(host)+" is not in server.allowed_hosts", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.Trim(h, "[]")
}

func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(host)
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case a == "":
			continue
		case strings.HasPrefix(a, "*."):
			if strings.HasSuffix(host, a[1:]) || host == a[2:] {
				return true
			}
		case a == host:
			return true
		}
	}
	return false
}

// csrf implements FR-SEC-02/03 for mutating API requests.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			next.ServeHTTP(w, r)
			return
		case http.MethodOptions:
			// Never answer CORS preflights positively: no Access-Control-* headers.
			writeError(w, http.StatusForbidden, "csrf_rejected", "cross-origin requests are not permitted", nil)
			return
		}
		if r.Header.Get("X-Requested-With") != CSRFHeaderValue {
			writeError(w, http.StatusForbidden, "csrf_rejected",
				"mutating requests require the header X-Requested-With: "+CSRFHeaderValue, nil)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			writeError(w, http.StatusForbidden, "csrf_rejected", "cross-site request rejected (Sec-Fetch-Site="+site+")", nil)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			if !s.originMatches(origin, r) {
				writeError(w, http.StatusForbidden, "csrf_rejected", "Origin does not match this server", nil)
				return
			}
		}
		if ct := r.Header.Get("Content-Type"); r.ContentLength != 0 && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request body must be application/json", nil)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // FR-SEC-05
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originMatches(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := r.Host
	if s.cfg.Server.TrustProxyHeaders {
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			host = strings.TrimSpace(strings.Split(fh, ",")[0])
		}
	}
	return strings.EqualFold(u.Host, host)
}
