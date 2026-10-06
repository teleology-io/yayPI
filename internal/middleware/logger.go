package middleware

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// responseWriter wraps http.ResponseWriter to capture the status code and size while
// still exposing Flusher/Hijacker to handlers that need them (SSE, websockets).
type responseWriter struct {
	http.ResponseWriter
	status      int
	size        int
	wroteHeader bool
}

func (rw *responseWriter) WriteHeader(status int) {
	if !rw.wroteHeader {
		rw.status, rw.wroteHeader = status, true
	}
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.status, rw.wroteHeader = http.StatusOK, true
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.size += n
	return n, err
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijacking not supported")
}

func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// RequestObserver receives one call per finished request (metrics).
type RequestObserver func(method, route string, status int, d time.Duration)

// Logger returns a structured request-logging middleware. It logs the matched route
// pattern (not the raw path with ids), never the query string (which may carry API keys
// or tokens), plus client IP, subject, request and trace ids. observe may be nil.
func Logger(logger zerolog.Logger, observe RequestObserver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			// Subject is set by auth middleware further down the chain; capture it via a
			// pointer the inner chain can fill.
			holder := &subjectHolder{}
			r = r.WithContext(withSubjectHolder(r.Context(), holder))

			next.ServeHTTP(rw, r)

			duration := time.Since(start)
			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			if observe != nil {
				observe(r.Method, route, rw.status, duration)
			}

			event := logger.Info()
			if rw.status >= 500 {
				event = logger.Error()
			} else if rw.status >= 400 {
				event = logger.Warn()
			}
			event = event.
				Str("request_id", GetRequestID(r)).
				Str("trace_id", GetTrace(r.Context()).TraceID).
				Str("method", r.Method).
				Str("route", route).
				Str("path", r.URL.Path).
				Int("status", rw.status).
				Int("size", rw.size).
				Dur("duration", duration).
				Str("client_ip", GetClientIP(r))
			if holder.sub != nil {
				event = event.Str("subject", holder.sub.ID)
			}
			event.Msg("request")
		})
	}
}

// DefaultLogger returns a Logger middleware using the global zerolog logger.
func DefaultLogger() func(http.Handler) http.Handler {
	return Logger(log.Logger, nil)
}
