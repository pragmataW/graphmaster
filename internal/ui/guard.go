package ui

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// contentSecurityPolicy allows only same-origin scripts and requests.
// Cytoscape sets element styles from script, which CSP does not restrict.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// guard wraps next with the checks every request must pass, in order:
// a loopback Host header (DNS rebinding), the URL token, and GET/HEAD only.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// The token is in the URL: never leak it through Referer or caches.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")

		if !allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if !s.validToken(r.URL.Query().Get("token")) {
			http.Error(w, "missing or invalid token", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHost accepts "localhost" and "127.0.0.1", with or without a port.
func allowedHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "127.0.0.1" || strings.EqualFold(host, "localhost")
}

// validToken compares in constant time.
func (s *Server) validToken(got string) bool {
	return len(got) == len(s.token) && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}
