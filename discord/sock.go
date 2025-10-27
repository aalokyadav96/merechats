package discord

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"naevis/db"
	"naevis/middleware"
	"naevis/models"

	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var (
	// clients maps userID => *Client
	clients = struct {
		sync.RWMutex
		m map[string]*Client
	}{m: make(map[string]*Client)}

	upgrader = websocket.Upgrader{
		// In production you should validate the Origin header.
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

// Client represents a connected websocket client with a send queue
type Client struct {
	UserID string
	Conn   *websocket.Conn
	Send   chan interface{} // buffered outbound queue
	// optional: add a mutex if you need to mutate Conn concurrently (we serialize writes via Send)
}

const (
	writeTimeout  = 10 * time.Second
	pongWait      = 60 * time.Second
	pingPeriod    = 30 * time.Second // must be < pongWait
	sendQueueSize = 256
)

// HandleWebSocket manages connections & messages
func HandleWebSocket(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	rawToken := r.URL.Query().Get("token")
	if rawToken == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	claims, err := middleware.ValidateJWT("Bearer " + rawToken)
	if err != nil {
		log.Println("WS: invalid token:", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	userID := claims.UserID
	log.Println("WS connected:", userID)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WS upgrade failed:", err)
		return
	}

	client := &Client{
		UserID: userID,
		Conn:   conn,
		Send:   make(chan interface{}, sendQueueSize),
	}

	// register client
	clients.Lock()
	clients.m[userID] = client
	clients.Unlock()

	// ensure cleanup on return
	done := make(chan struct{})
	defer func() {
		close(done)
		// unregister and close
		clients.Lock()
		if c, ok := clients.m[userID]; ok {
			delete(clients.m, userID)
			// close send channel to stop writer goroutine
			close(c.Send)
		}
		clients.Unlock()
		_ = conn.Close()
		log.Println("WS disconnected:", userID)
	}()

	// Setup pong handler and initial read deadline
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(appData string) error {
		_ = appData
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// writer goroutine: serializes writes to this connection
	go func() {
		for msg := range client.Send {
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteJSON(msg); err != nil {
				log.Printf("WS write error for %s: %v", userID, err)
				// closing connection will cause reader to exit and cleanup
				_ = conn.Close()
				return
			}
		}
	}()

	// Heartbeat ping goroutine
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Use Control to send Ping or WriteControl so it doesn't interfere with writer queue
				conn.SetWriteDeadline(time.Now().Add(writeTimeout))
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
					// ping failure — close connection
					_ = conn.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Reader loop
	for {
		var in models.IncomingWSMessage
		// Note: ReadJSON will block until message arrives or deadline/pong fails.
		if err := conn.ReadJSON(&in); err != nil {
			log.Printf("WS read error (%s): %v", userID, err)
			break
		}

		switch in.Type {
		case "message":
			handleIncomingMessage(ctx, client, in)
		case "typing":
			broadcastToChat(ctx, in.ChatID, map[string]interface{}{
				"type":   "typing",
				"sender": userID,
				"chatid": in.ChatID,
			})
		case "presence":
			broadcastGlobal(map[string]interface{}{
				"type":   "presence",
				"from":   userID,
				"online": in.Online,
			})
		default:
			log.Printf("WS unknown type from %s: %s", userID, in.Type)
		}
	}
}

// handleIncomingMessage now accepts *Client to use its send queue if needed.
func handleIncomingMessage(ctx context.Context, client *Client, in models.IncomingWSMessage) {
	cid := in.ChatID
	userID := client.UserID

	// verify user belongs to chat (chatid used consistently)
	count, err := db.MereCollection.CountDocuments(ctx, bson.M{"chatid": cid, "participants": userID})
	if err != nil {
		log.Printf("WS membership check failed (%s): %v", userID, err)
		return
	}
	if count == 0 {
		log.Printf("WS unauthorized chat access (%s): %s", userID, in.ChatID)
		return
	}

	msg, err := persistMessage(ctx, cid, userID, in.Content, in.MediaURL, in.MediaType)
	if err != nil {
		log.Printf("WS persist error (%s): %v", userID, err)
		return
	}

	payload := map[string]interface{}{
		"type":      "message",
		"id":        msg.ID.Hex(),
		"sender":    msg.UserID,
		"content":   msg.Content,
		"createdAt": msg.CreatedAt,
		"media":     msg.Media,
		"chatid":    msg.ChatID,
	}
	if in.ClientID != "" {
		payload["clientId"] = in.ClientID
	}

	broadcastToChat(ctx, cid, payload)
}

//
// ==== Broadcasting ====
//

func broadcastToChat(ctx context.Context, chatHex string, payload interface{}) {
	cid := chatHex
	var chat models.Chat
	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": cid}).Decode(&chat); err != nil {
		log.Printf("WS broadcast chat not found: %v", cid)
		return
	}

	clients.RLock()
	targets := make(map[string]*Client, len(chat.Participants))
	for _, p := range chat.Participants {
		if c, ok := clients.m[p]; ok {
			targets[p] = c
		}
	}
	clients.RUnlock()

	for uid, client := range targets {
		// non-blocking send: drop if the client's send buffer is full
		select {
		case client.Send <- payload:
		default:
			// slow client; drop message and optionally log
			log.Printf("WS dropping message to %s (slow client)", uid)
		}
	}
}

func broadcastGlobal(payload interface{}) {
	clients.RLock()
	conns := make([]*Client, 0, len(clients.m))
	for _, c := range clients.m {
		conns = append(conns, c)
	}
	clients.RUnlock()

	for _, client := range conns {
		select {
		case client.Send <- payload:
		default:
			log.Printf("WS dropping global message to %s (slow client)", client.UserID)
		}
	}
}

//
// ==== Persistence ====
//

func persistMediaMessage(ctx context.Context, chatID string, sender, mediaURL, mediaType string) (*models.Message, error) {
	return persistMessage(ctx, chatID, sender, "", mediaURL, mediaType)
}

func persistMessage(ctx context.Context, chatID string, sender, content, mediaURL, mediaType string) (*models.Message, error) {
	if content == "" && mediaURL == "" {
		return nil, errors.New("empty content and media")
	}

	var media *models.Media
	if mediaURL != "" && mediaType != "" {
		media = &models.Media{URL: mediaURL, Type: mediaType}
	}

	msg := &models.Message{
		ChatID:    chatID,
		UserID:    sender,
		Content:   content,
		Media:     media,
		CreatedAt: time.Now(),
	}

	res, err := db.MessagesCollection.InsertOne(ctx, msg)
	if err != nil {
		return nil, err
	}
	msg.ID = res.InsertedID.(primitive.ObjectID)

	// update chat's updatedAt by chatid
	_, _ = db.MereCollection.UpdateOne(ctx,
		bson.M{"chatid": chatID},
		bson.M{"$set": bson.M{"updatedAt": time.Now()}},
	)
	return msg, nil
}

//
// ==== Misc ===
//

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func writeErr(w http.ResponseWriter, msg string, code int) {
	http.Error(w, msg, code)
}

// package discord

// import (
// 	"context"
// 	"errors"
// 	"log"
// 	"net/http"
// 	"strconv"
// 	"sync"
// 	"time"

// 	"naevis/db"
// 	"naevis/middleware"

// 	"github.com/gorilla/websocket"
// 	"github.com/julienschmidt/httprouter"
// 	"go.mongodb.org/mongo-driver/bson"
// 	"go.mongodb.org/mongo-driver/bson/primitive"
// )

// var (
// 	clients = struct {
// 		sync.RWMutex
// 		m map[string]*websocket.Conn
// 	}{m: make(map[string]*websocket.Conn)}

// 	upgrader = websocket.Upgrader{
// 		CheckOrigin: func(r *http.Request) bool { return true },
// 	}
// )

// // HandleWebSocket manages connections & messages
// func HandleWebSocket(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
// 	ctx := r.Context()
// 	rawToken := r.URL.Query().Get("token")
// 	if rawToken == "" {
// 		http.Error(w, "missing token", http.StatusUnauthorized)
// 		return
// 	}

// 	claims, err := middleware.ValidateJWT("Bearer " + rawToken)
// 	if err != nil {
// 		log.Println("WS: invalid token:", err)
// 		http.Error(w, "unauthorized", http.StatusUnauthorized)
// 		return
// 	}
// 	userID := claims.UserID
// 	log.Println("WS connected:", userID)

// 	conn, err := upgrader.Upgrade(w, r, nil)
// 	if err != nil {
// 		log.Println("WS upgrade failed:", err)
// 		return
// 	}

// 	clients.Lock()
// 	clients.m[userID] = conn
// 	clients.Unlock()

// 	done := make(chan struct{})

// 	defer func() {
// 		close(done)
// 		clients.Lock()
// 		delete(clients.m, userID)
// 		clients.Unlock()
// 		_ = conn.Close()
// 		log.Println("WS disconnected:", userID)
// 	}()

// 	// Heartbeat ping
// 	go func() {
// 		ticker := time.NewTicker(30 * time.Second)
// 		defer ticker.Stop()
// 		for {
// 			select {
// 			case <-ticker.C:
// 				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
// 				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
// 					return
// 				}
// 			case <-done:
// 				return
// 			}
// 		}
// 	}()

// 	for {
// 		var in IncomingWSMessage
// 		if err := conn.ReadJSON(&in); err != nil {
// 			log.Printf("WS read error (%s): %v", userID, err)
// 			break
// 		}

// 		switch in.Type {
// 		case "message":
// 			handleIncomingMessage(ctx, conn, userID, in)
// 		case "typing":
// 			broadcastToChat(ctx, in.ChatID, map[string]interface{}{
// 				"type":   "typing",
// 				"sender": userID,
// 				"chatid": in.ChatID,
// 			})
// 		case "presence":
// 			broadcastGlobal(map[string]interface{}{
// 				"type":   "presence",
// 				"from":   userID,
// 				"online": in.Online,
// 			})
// 		default:
// 			log.Printf("WS unknown type from %s: %s", userID, in.Type)
// 		}
// 	}
// }

// //
// // ==== Helpers ====
// //

// func handleIncomingMessage(ctx context.Context, conn *websocket.Conn, userID string, in IncomingWSMessage) {
// 	_ = conn
// 	cid := in.ChatID

// 	// verify user belongs to chat
// 	count, err := db.MereCollection.CountDocuments(ctx, bson.M{"_id": cid, "participants": userID})
// 	if err != nil || count == 0 {
// 		log.Printf("WS unauthorized chat access (%s): %s", userID, in.ChatID)
// 		return
// 	}

// 	msg, err := persistMessage(ctx, cid, userID, in.Content, in.MediaURL, in.MediaType)
// 	if err != nil {
// 		log.Printf("WS persist error (%s): %v", userID, err)
// 		return
// 	}

// 	payload := map[string]interface{}{
// 		"type":      "message",
// 		"id":        msg.ID.Hex(),
// 		"sender":    msg.Sender,
// 		"content":   msg.Content,
// 		"createdAt": msg.CreatedAt,
// 		"media":     msg.Media,
// 	}
// 	if in.ClientID != "" {
// 		payload["clientId"] = in.ClientID
// 	}

// 	broadcastToChat(ctx, in.ChatID, payload)
// }

// //
// // ==== Broadcasting ====
// //

// func broadcastToChat(ctx context.Context, chatHex string, payload interface{}) {
// 	cid := chatHex
// 	var chat Chat
// 	if err := db.MereCollection.FindOne(ctx, bson.M{"_id": cid}).Decode(&chat); err != nil {
// 		log.Printf("WS broadcast chat not found: %v", cid)
// 		return
// 	}

// 	clients.RLock()
// 	targets := make(map[string]*websocket.Conn, len(chat.Participants))
// 	for _, p := range chat.Participants {
// 		if c, ok := clients.m[p]; ok {
// 			targets[p] = c
// 		}
// 	}
// 	clients.RUnlock()

// 	for uid, conn := range targets {
// 		go safeWriteJSON(uid, conn, payload)
// 	}
// }

// func broadcastGlobal(payload interface{}) {
// 	clients.RLock()
// 	conns := make(map[string]*websocket.Conn, len(clients.m))
// 	for id, conn := range clients.m {
// 		conns[id] = conn
// 	}
// 	clients.RUnlock()

// 	for id, conn := range conns {
// 		go safeWriteJSON(id, conn, payload)
// 	}
// }

// // Safe write to WS
// func safeWriteJSON(uid string, conn *websocket.Conn, payload interface{}) {
// 	defer func() {
// 		if r := recover(); r != nil {
// 			log.Printf("WS write panic for %s: %v", uid, r)
// 		}
// 	}()
// 	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
// 	if err := conn.WriteJSON(payload); err != nil {
// 		log.Printf("WS write error for %s: %v", uid, err)
// 		conn.Close()
// 		clients.Lock()
// 		delete(clients.m, uid)
// 		clients.Unlock()
// 	}
// }

// //
// // ==== Persistence ====
// //

// func persistMediaMessage(ctx context.Context, chatID string, sender, mediaURL, mediaType string) (*Message, error) {
// 	return persistMessage(ctx, chatID, sender, "", mediaURL, mediaType)
// }

// func persistMessage(ctx context.Context, chatID string, sender, content, mediaURL, mediaType string) (*Message, error) {
// 	if content == "" && mediaURL == "" {
// 		return nil, errors.New("empty content and media")
// 	}

// 	var media *Media
// 	if mediaURL != "" && mediaType != "" {
// 		media = &Media{URL: mediaURL, Type: mediaType}
// 	}

// 	msg := &Message{
// 		ChatID:    chatID,
// 		Sender:    sender,
// 		Content:   content,
// 		Media:     media,
// 		CreatedAt: time.Now(),
// 	}

// 	res, err := db.MessagesCollection.InsertOne(ctx, msg)
// 	if err != nil {
// 		return nil, err
// 	}
// 	msg.ID = res.InsertedID.(primitive.ObjectID)

// 	_, _ = db.MereCollection.UpdateOne(ctx,
// 		bson.M{"chatid": chatID},
// 		bson.M{"$set": bson.M{"updatedAt": time.Now()}},
// 	)
// 	return msg, nil
// }

// //
// // ==== Misc ===
// //

// func parseInt64(s string) (int64, error) {
// 	return strconv.ParseInt(s, 10, 64)
// }

// func writeErr(w http.ResponseWriter, msg string, code int) {
// 	http.Error(w, msg, code)
// }
