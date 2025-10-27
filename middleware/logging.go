package middleware

import (
	"log"
	"net/http"
	"time"
)

// loggingMiddleware logs each request method, path, remote address, and duration.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		duration := time.Since(start)
		log.Printf("%s %s from %s – %v", r.Method, r.RequestURI, r.RemoteAddr, duration)
	})
}
