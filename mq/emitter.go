package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"naevis/models"
	"naevis/rdx"
)

// Index represents an indexing-related message to be emitted.
type Index struct {
	EntityType string `json:"entity_type"`
	Method     string `json:"method"`
	EntityId   string `json:"entity_id"`
	ItemId     string `json:"item_id"`
	ItemType   string `json:"item_type"`
}

// Notify is a placeholder for broadcasting events without indexing.
func Notify(eventName string, content models.Index) error {
	fmt.Println(eventName, "Notified", content)
	return nil
}

// Emit now publishes indexing events to Redis instead of running immediately
func Emit(ctx context.Context, eventName string, content models.Index) {
	log.Printf("[Emit] START eventName=%s content=%+v", eventName, content)

	data, err := json.Marshal(content)
	if err != nil {
		log.Printf("[Emit] Failed to marshal event content: %v", err)
		return
	}

	if err := rdx.Conn.Publish(context.Background(), "indexing-events", data).Err(); err != nil {
		log.Printf("[Emit] Failed to publish event to Redis: %v", err)
		return
	}

	log.Printf("[Emit] Event published to channel 'indexing-events'")
	log.Printf("[Emit] END")
}
