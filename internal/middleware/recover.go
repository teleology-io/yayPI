package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/rs/zerolog/log"

	"github.com/teleology-io/yayPI/internal/apierr"
)

// Recover is a panic recovery middleware that logs the stack and returns a 500.
// http.ErrAbortHandler is re-panicked so net/http can abort the connection as intended.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			reqID := GetRequestID(r)
			log.Error().
				Str("request_id", reqID).
				Interface("panic", rec).
				Bytes("stack", debug.Stack()).
				Msg("recovered from panic")
			apierr.Write(w, reqID, http.StatusInternalServerError, "internal server error")
		}()
		next.ServeHTTP(w, r)
	})
}
