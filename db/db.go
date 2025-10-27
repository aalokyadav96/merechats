package db

import (
	"context"
	"log"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	Client *mongo.Client
	// Your collections:
	ChatsCollection    *mongo.Collection
	MereCollection     *mongo.Collection
	MessagesCollection *mongo.Collection
)

// limiter chan to cap concurrent Mongo ops
var mongoLimiter = make(chan struct{}, 100) // allow up to 100 concurrent ops

func init() {
	_ = godotenv.Load()

	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		log.Fatal("❌ MONGODB_URI environment variable not set")
	}

	clientOpts := options.Client().
		ApplyURI(uri).
		SetMaxPoolSize(100).
		SetMinPoolSize(10).
		SetRetryWrites(true)

	var err error
	Client, err = mongo.Connect(context.Background(), clientOpts)
	if err != nil {
		log.Fatalf("❌ Failed to connect to MongoDB: %v", err)
	}
	if err := Client.Ping(context.Background(), nil); err != nil {
		log.Fatalf("❌ Mongo ping failed: %v", err)
	}

	log.Printf("✅ MongoDB connected (%s) maxPool=%d minPool=%d; Goroutines at start: %d",
		uri, *clientOpts.MaxPoolSize, *clientOpts.MinPoolSize, runtime.NumGoroutine(),
	)

	// Graceful shutdown hook
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		log.Println("🛑 Disconnecting from MongoDB...")
		_ = Client.Disconnect(context.Background())
		os.Exit(0)
	}()

	// Optional: log connection stats periodically
	go logPoolStats()

	// Initialize your collections
	db := Client.Database("eventdb")
	ChatsCollection = db.Collection("chats")
	MereCollection = db.Collection("mere")
	MessagesCollection = db.Collection("messages")
}

// logPoolStats logs basic goroutine and pool stats every 60s (optional)
func logPoolStats() {
	for {
		time.Sleep(60 * time.Second)
		log.Printf("📊 Mongo Stats: Goroutines=%d | MongoOpsRunning=%d", runtime.NumGoroutine(), len(mongoLimiter))
	}
}

// PingMongo can be used in your /health endpoint
func PingMongo() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return Client.Ping(ctx, nil)
}

// WithMongo wraps any Mongo operation with concurrency and timeout + minimal retry
func WithMongo(op func(ctx context.Context) error) error {
	mongoLimiter <- struct{}{}        // acquire slot
	defer func() { <-mongoLimiter }() // release slot

	var err error
	for i := 0; i < 2; i++ { // 1 retry max
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err = op(ctx)
		if err == nil {
			return nil
		}
		log.Printf("⚠️ Mongo op failed: %v (retry %d)", err, i+1)
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// OptionsFindLatest provides a find option with latest sort
func OptionsFindLatest(limit int64) *options.FindOptions {
	opts := options.Find()
	opts.SetSort(map[string]interface{}{"createdAt": -1})
	opts.SetLimit(limit)
	return opts
}
