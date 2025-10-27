package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
// The chat routes will be added separately in main to avoid passing hub around globally.
func setupRouter(rateLimiter *ratelim.RateLimiter) *httprouter.Router {
	router := httprouter.New()
	router.GET("/health", Index)
	routes.RoutesWrapper(router, rateLimiter)
	return router
}

func main() {
	// load .env if present
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found; using system environment")
	}

	// read port
	port := os.Getenv("PORT")
	if port == "" {
		port = ":10000"
	} else if port[0] != ':' {
		port = ":" + port
	}

	// initialize rate limiter
	rateLimiter := ratelim.NewRateLimiter(1, 6, 10*time.Minute, 10000)

	// build router and add chat routes with hub
	router := setupRouter(rateLimiter)

	// apply middleware: CORS → security headers → logging → router
	corsHandler := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"}, // lock down in production
		AllowedMethods:   []string{"HEAD", "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "Idempotency-Key"},
		AllowCredentials: false,
	}).Handler(router)

	handler := middleware.LoggingMiddleware(middleware.SecurityHeaders(corsHandler))

	// create HTTP server
	server := &http.Server{
		Addr:              port,
		Handler:           handler,
		ReadTimeout:       7 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
	}

	// on shutdown: stop chat hub, cleanup
	server.RegisterOnShutdown(func() {
		log.Println("🛑 Shutting down...")
	})

	// start server
	go func() {
		log.Printf("🚀 Server listening on %s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ ListenAndServe error: %v", err)
		}
	}()

	// wait for interrupt or SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	// initiate graceful shutdown
	log.Println("🛑 Shutdown signal received; shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("❌ Graceful shutdown failed: %v", err)
	}

	log.Println("✅ Server stopped cleanly")
}

/*
func withCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'")
		next.ServeHTTP(w, r)
	})
}
router := httprouter.New()
wrapped := withCSP(router)
log.Fatal(http.ListenAndServe(":8080", wrapped))

*/
