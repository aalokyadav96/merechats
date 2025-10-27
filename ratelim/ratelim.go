package ratelim

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/julienschmidt/httprouter"
	"golang.org/x/time/rate"
)

// RateLimiter is a middleware struct with configuration and visitor state
type RateLimiter struct {
	visitors     map[string]*rate.Limiter
	mu           sync.Mutex
	rate         rate.Limit
	burst        int
	cleanupAfter time.Duration
	maxEntries   int
}

// NewRateLimiter initializes a new RateLimiter
func NewRateLimiter(r rate.Limit, b int, cleanupAfter time.Duration, maxEntries int) *RateLimiter {
	return &RateLimiter{
		visitors:     make(map[string]*rate.Limiter),
		rate:         r,
		burst:        b,
		cleanupAfter: cleanupAfter,
		maxEntries:   maxEntries,
	}
}

// getLimiter returns an existing limiter or creates a new one
func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if limiter, exists := rl.visitors[ip]; exists {
		return limiter
	}

	// Optional: Enforce max entries to avoid memory abuse
	if len(rl.visitors) >= rl.maxEntries {
		// Reject new IPs silently, or fallback to a shared limiter
		return rate.NewLimiter(rate.Limit(0.1), 1) // harsh fallback
	}

	limiter := rate.NewLimiter(rl.rate, rl.burst)
	rl.visitors[ip] = limiter

	// Cleanup this IP entry after a fixed TTL
	go func() {
		time.Sleep(rl.cleanupAfter)
		rl.mu.Lock()
		delete(rl.visitors, ip)
		rl.mu.Unlock()
	}()

	return limiter
}

// extractClientIP tries to determine the client's real IP address
func extractClientIP(r *http.Request) string {
	// Respect reverse proxy headers
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	// Otherwise use RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// Limit is the httprouter middleware for rate limiting
func (rl *RateLimiter) Limit(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		ip := extractClientIP(r)
		limiter := rl.getLimiter(ip)

		if !limiter.Allow() {
			// Optional logging could go here
			http.Error(w, "Too many requests. Please try again later.", http.StatusTooManyRequests)
			return
		}

		next(w, r, ps)
	}
}
