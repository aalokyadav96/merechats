package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"naevis/middleware"
	"naevis/ratelim"
	"naevis/routes"

	"github.com/joho/godotenv"
	"github.com/julienschmidt/httprouter"
	"github.com/rs/cors"
)

// Index is a simple health check handler.
func Index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	fmt.Fprint(w, "200")
}

// setupRouter builds the router with all routes except chat.
func setupRouter(rateLimiter *ratelim.RateLimiter) *httprouter.Router {
	router := httprouter.New()
	router.GET("/health", Index)
	routes.RoutesWrapper(router, rateLimiter)
	return router
}

// parseAllowedOrigins safely parses allowed origins from environment variable.
func parseAllowedOrigins(env string) []string {
	if env == "" {
		return []string{"http://localhost:5173", "https://indium.netlify.app"}
	}
	parts := strings.Split(env, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found; using system environment")
	}

	// Read port
	port := os.Getenv("PORT")
	if port == "" {
		port = ":10000"
	} else if port[0] != ':' {
		port = ":" + port
	}

	// Parse allowed origins
	allowedOrigins := parseAllowedOrigins(os.Getenv("ALLOWED_ORIGINS"))

	// Initialize rate limiter
	rateLimiter := ratelim.NewRateLimiter(1, 6, 10*time.Minute, 10000)

	// Build router
	router := setupRouter(rateLimiter)
	// routes.AddStaticRoutes(router)

	// Apply middleware stack: Security → Logging → CORS
	innerHandler := middleware.LoggingMiddleware(middleware.SecurityHeaders(router))
	corsHandler := cors.New(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"HEAD", "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "Idempotency-Key", "X-Requested-With"},
		AllowCredentials: true,
	}).Handler(innerHandler)

	// Multiplexer: CORS applied selectively
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "200")
	})
	mux.Handle("/", corsHandler)

	// Configure HTTP server
	server := &http.Server{
		Addr:              port,
		Handler:           mux,
		ReadTimeout:       7 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
	}

	server.RegisterOnShutdown(func() {
		log.Println("Shutting down...")
	})

	// Start server
	go func() {
		log.Printf("Server listening on %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe error: %v", err)
		}
	}()

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutdown signal received; shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Graceful shutdown failed: %v", err)
	}

	log.Println("Server stopped cleanly")
}
