package rdx

import (
	"encoding/json"
	"log"
	"naevis/db"
	"naevis/globals"
	"naevis/models"
	"time"
)

// Flush messages from Redis to MongoDB in bulk.
func FlushRedisMessages() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		// Get all keys matching chat:*:messages.
		keys, err := Conn.Keys(globals.Ctx, "chat:*:messages").Result()
		if err != nil {
			log.Println("Redis scan error:", err)
			continue
		}
		for _, key := range keys {
			// Retrieve all messages from Redis.
			msgs, err := Conn.LRange(globals.Ctx, key, 0, -1).Result()
			if err != nil {
				log.Println("Redis LRange error:", err)
				continue
			}
			if len(msgs) == 0 {
				continue
			}
			var messagesBulk []interface{}
			for _, mStr := range msgs {
				var m models.Message
				if err := json.Unmarshal([]byte(mStr), &m); err != nil {
					log.Println("JSON unmarshal error:", err)
					continue
				}
				messagesBulk = append(messagesBulk, m)
			}
			if len(messagesBulk) > 0 {
				_, err := db.MessagesCollection.InsertMany(globals.Ctx, messagesBulk)
				if err != nil {
					log.Println("MongoDB InsertMany error:", err)
					continue
				}
				// Remove the key from Redis after successful insertion.
				Conn.Del(globals.Ctx, key)
			}
		}
	}
}
