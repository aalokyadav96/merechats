package discord

import (
	"encoding/json"
	"naevis/db"
	"naevis/models"
	"naevis/utils"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EditMessage enforces that only the message sender can edit
func EditMessage(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	msgID, err := primitive.ObjectIDFromHex(ps.ByName("messageid"))
	if err != nil {
		writeErr(w, "invalid messageId", http.StatusBadRequest)
		return
	}

	var existing models.Message
	if err := db.MessagesCollection.FindOne(ctx, bson.M{"_id": msgID}).Decode(&existing); err != nil {
		if err == mongo.ErrNoDocuments {
			writeErr(w, "message not found", http.StatusNotFound)
			return
		}
		writeErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	// permission check
	if existing.UserID != user {
		writeErr(w, "forbidden", http.StatusForbidden)
		return
	}

	var body struct{ Content string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Content = strings.TrimSpace(body.Content)
	if body.Content == "" {
		writeErr(w, "content required", http.StatusBadRequest)
		return
	}
	now := time.Now()
	res, err := db.MessagesCollection.UpdateOne(ctx,
		bson.M{"_id": msgID},
		bson.M{"$set": bson.M{"content": body.Content, "editedAt": now}},
	)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if res.MatchedCount == 0 {
		writeErr(w, "not found or no permission", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteMessage enforces that only the message sender can delete
func DeleteMessage(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	msgID, err := primitive.ObjectIDFromHex(ps.ByName("messageid"))
	if err != nil {
		writeErr(w, "invalid messageId", http.StatusBadRequest)
		return
	}

	var existing models.Message
	if err := db.MessagesCollection.FindOne(ctx, bson.M{"_id": msgID}).Decode(&existing); err != nil {
		if err == mongo.ErrNoDocuments {
			writeErr(w, "message not found", http.StatusNotFound)
			return
		}
		writeErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	// permission check: only sender can soft-delete
	if existing.UserID != user {
		writeErr(w, "forbidden", http.StatusForbidden)
		return
	}

	res, err := db.MessagesCollection.UpdateOne(ctx,
		bson.M{"_id": msgID},
		bson.M{"$set": bson.M{"deleted": true}},
	)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if res.MatchedCount == 0 {
		writeErr(w, "not found or no permission", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func SearchMessages(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	chatID := ps.ByName("chatid")
	// verify access
	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Err(); err != nil {
		if err == mongo.ErrNoDocuments {
			writeErr(w, "not found or access denied", http.StatusNotFound)
			return
		}
		writeErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	term := r.URL.Query().Get("term")

	// pagination
	limit := int64(50)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := parseInt64(l); err == nil && v > 0 {
			limit = v
		}
	}
	skip := int64(0)
	if s := r.URL.Query().Get("skip"); s != "" {
		if v, err := parseInt64(s); err == nil && v >= 0 {
			skip = v
		}
	}

	filter := bson.M{"chatid": chatID, "deleted": bson.M{"$ne": true}}
	if term != "" {
		filter["content"] = bson.M{"$regex": primitive.Regex{Pattern: term, Options: "i"}}
	}

	opts := options.Find().
		SetSort(bson.M{"createdAt": 1}).
		SetLimit(limit).
		SetSkip(skip)

	cursor, err := db.MessagesCollection.Find(ctx, filter, opts)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var msgs []models.Message
	if err := cursor.All(ctx, &msgs); err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if msgs == nil {
		msgs = make([]models.Message, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(msgs); err != nil {
		writeErr(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

// GetUnreadCount returns unread counts per chat the user participates in.
// Uses an aggregation for message counts and merges results with the chat list so chats with zero unread are included.
func GetUnreadCount(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	user := utils.GetUserIDFromRequest(r)
	ctx := r.Context()

	// First, retrieve chats the user participates in
	cursor, err := db.MereCollection.Find(ctx, bson.M{"participants": user})
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var chats []models.Chat
	if err := cursor.All(ctx, &chats); err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Aggregation: group unread, non-deleted messages by chatid
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "deleted", Value: bson.D{{Key: "$ne", Value: true}}},
			{Key: "readBy", Value: bson.D{{Key: "$ne", Value: user}}},
		}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$chatid"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}

	aggCursor, err := db.MessagesCollection.Aggregate(ctx, pipeline)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer aggCursor.Close(ctx)

	type aggRes struct {
		ID    string `bson:"_id"`
		Count int64  `bson:"count"`
	}

	countMap := make(map[string]int64, 0)
	for aggCursor.Next(ctx) {
		var a aggRes
		if err := aggCursor.Decode(&a); err != nil {
			continue
		}
		countMap[a.ID] = a.Count
	}

	type Unread struct {
		ChatID string `json:"chatid"`
		Count  int64  `json:"count"`
	}
	var result []Unread
	for _, chat := range chats {
		c := countMap[chat.ChatID]
		result = append(result, Unread{ChatID: chat.ChatID, Count: c})
	}
	if result == nil {
		result = make([]Unread, 0)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		writeErr(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func MarkAsRead(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	msgID, err := primitive.ObjectIDFromHex(ps.ByName("messageid"))
	if err != nil {
		writeErr(w, "invalid messageId", http.StatusBadRequest)
		return
	}
	user := utils.GetUserIDFromRequest(r)

	res, err := db.MessagesCollection.UpdateOne(ctx,
		bson.M{"_id": msgID},
		bson.M{"$addToSet": bson.M{"readBy": user}},
	)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if res.MatchedCount == 0 {
		writeErr(w, "message not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UploadAttachment handles media/file upload into a chat
func UploadAttachment(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	chatID := ps.ByName("chatid")

	contentType := r.FormValue("contenttype")
	savedName := r.FormValue("savedname")

	// Ensure user is participant of the chat
	var chat models.Chat
	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Decode(&chat); err != nil {
		if err == mongo.ErrNoDocuments {
			writeErr(w, "chat not found or access denied", http.StatusNotFound)
			return
		}
		writeErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Persist media message
	msg, err := persistMediaMessage(ctx, chatID, user, savedName, contentType)
	if err != nil {
		writeErr(w, "failed to persist message", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(msg); err != nil {
		// encoding failed
		writeErr(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

// // UploadAttachment handles media/file upload into a chat
// func UploadAttachment(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)

// 	chatID := ps.ByName("chatid")

// 	// Ensure user is participant of the chat
// 	var chat models.Chat
// 	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Decode(&chat); err != nil {
// 		if err == mongo.ErrNoDocuments {
// 			writeErr(w, "chat not found or access denied", http.StatusNotFound)
// 			return
// 		}
// 		writeErr(w, "internal error", http.StatusInternalServerError)
// 		return
// 	}

// 	// Allow up to 50MB by default
// 	if err := r.ParseMultipartForm(50 << 20); err != nil {
// 		writeErr(w, "invalid form", http.StatusBadRequest)
// 		return
// 	}

// 	var header *multipart.FileHeader
// 	if r.MultipartForm != nil && r.MultipartForm.File != nil {
// 		files := r.MultipartForm.File["file"]
// 		if len(files) > 0 {
// 			header = files[0]
// 		}
// 	}
// 	if header == nil {
// 		writeErr(w, "no file provided", http.StatusBadRequest)
// 		return
// 	}

// 	// Try to determine content type more reliably by peeking into the file.
// 	contentType := header.Header.Get("Content-Type")
// 	// If header is missing or generic, sniff first 512 bytes.
// 	if contentType == "" || contentType == "application/octet-stream" {
// 		f, err := header.Open()
// 		if err == nil {
// 			defer f.Close()
// 			buf := make([]byte, 512)
// 			n, _ := f.Read(buf)
// 			if n > 0 {
// 				contentType = http.DetectContentType(buf[:n])
// 			}
// 		}
// 		// if we couldn't open or sniff, fall back to header
// 		if contentType == "" {
// 			contentType = header.Header.Get("Content-Type")
// 		}
// 	}

// 	// Map content type → PictureType
// 	var picType filemgr.PictureType
// 	switch {
// 	case strings.HasPrefix(contentType, "image/"):
// 		picType = filemgr.PicPhoto
// 	case strings.HasPrefix(contentType, "video/"):
// 		picType = filemgr.PicVideo
// 	case strings.HasPrefix(contentType, "application/"), strings.HasPrefix(contentType, "text/"):
// 		picType = filemgr.PicFile
// 	default:
// 		writeErr(w, "unsupported file type", http.StatusBadRequest)
// 		return
// 	}

// 	// Save file via filemgr
// 	savedName, err := filemgr.SaveFormFile(r.MultipartForm, "file", filemgr.EntityChat, picType, false)
// 	if err != nil {
// 		writeErr(w, "cannot save file", http.StatusInternalServerError)
// 		return
// 	}

// 	// Persist media message
// 	msg, err := persistMediaMessage(ctx, chatID, user, savedName, contentType)
// 	if err != nil {
// 		writeErr(w, "failed to persist message", http.StatusInternalServerError)
// 		return
// 	}

//		w.Header().Set("Content-Type", "application/json")
//		if err := json.NewEncoder(w).Encode(msg); err != nil {
//			// encoding failed
//			writeErr(w, "failed to encode response", http.StatusInternalServerError)
//			return
//		}
//	}
func GetUserChats(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	skipStr := r.URL.Query().Get("skip")
	limitStr := r.URL.Query().Get("limit")

	var skip int64 = 0
	var limit int64 = 20

	if skipStr != "" {
		if val, err := strconv.ParseInt(skipStr, 10, 64); err == nil && val >= 0 {
			skip = val
		}
	}

	if limitStr != "" {
		if val, err := strconv.ParseInt(limitStr, 10, 64); err == nil && val > 0 {
			limit = val
		}
	}

	findOpts := options.Find().SetSkip(skip).SetLimit(limit).SetSort(bson.D{{Key: "updatedAt", Value: -1}})

	cursor, err := db.MereCollection.Find(ctx, bson.M{"participants": user}, findOpts)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var chats []models.Chat
	if err := cursor.All(ctx, &chats); err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if chats == nil {
		chats = make([]models.Chat, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(chats); err != nil {
		writeErr(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

// func GetUserChats(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)
// 	cursor, err := db.MereCollection.Find(ctx, bson.M{"participants": user})
// 	if err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	defer cursor.Close(ctx)

//		var chats []models.Chat
//		if err := cursor.All(ctx, &chats); err != nil {
//			writeErr(w, err.Error(), http.StatusInternalServerError)
//			return
//		}
//		if chats == nil {
//			chats = make([]models.Chat, 0)
//		}
//		w.Header().Set("Content-Type", "application/json")
//		if err := json.NewEncoder(w).Encode(chats); err != nil {
//			writeErr(w, "failed to encode response", http.StatusInternalServerError)
//			return
//		}
//	}
func StartNewChat(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	var body struct {
		Participants []string `json:"participants"`
		EntityType   string   `json:"entityType"`
		EntityId     string   `json:"entityId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}

	if len(body.Participants) == 0 {
		writeErr(w, "participants required", http.StatusBadRequest)
		return
	}

	// Deduplicate and include requester
	seen := make(map[string]struct{})
	var participants []string
	for _, p := range body.Participants {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			participants = append(participants, p)
		}
	}
	if _, ok := seen[user]; !ok {
		participants = append(participants, user)
	}

	if len(participants) == 0 {
		writeErr(w, "no valid participants", http.StatusBadRequest)
		return
	}

	// Sort participants for consistent array ordering
	sort.Strings(participants)

	// Exact match query (array equality)
	filter := bson.M{
		"participants": participants,
	}
	if body.EntityType != "" {
		filter["entityType"] = body.EntityType
	}
	if body.EntityId != "" {
		filter["entityId"] = body.EntityId
	}

	var existing models.Chat
	err := db.MereCollection.FindOne(ctx, filter).Decode(&existing)
	if err == nil {
		// Chat already exists
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(existing)
		return
	} else if err != mongo.ErrNoDocuments {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create new chat
	now := time.Now()
	newChat := models.Chat{
		ChatID:       utils.GenerateRandomString(16),
		Participants: participants,
		EntityType:   body.EntityType,
		EntityId:     body.EntityId,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	_, err = db.MereCollection.InsertOne(ctx, newChat)
	if err != nil {
		writeErr(w, "failed to create chat", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(newChat)
}

// func StartNewChat(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)

// 	var body struct {
// 		Participants []string `json:"participants"`
// 		EntityType   string   `json:"entityType"`
// 		EntityId     string   `json:"entityId"`
// 	}

// 	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
// 		writeErr(w, "invalid body", http.StatusBadRequest)
// 		return
// 	}

// 	if len(body.Participants) == 0 {
// 		writeErr(w, "participants required", http.StatusBadRequest)
// 		return
// 	}

// 	// Deduplicate and ensure requester included
// 	seen := make(map[string]struct{}, len(body.Participants)+1)
// 	var participants []string
// 	for _, p := range body.Participants {
// 		if p == "" {
// 			continue
// 		}
// 		if _, ok := seen[p]; ok {
// 			continue
// 		}
// 		seen[p] = struct{}{}
// 		participants = append(participants, p)
// 	}

// 	if _, ok := seen[user]; !ok {
// 		participants = append(participants, user)
// 	}

// 	if len(participants) == 0 {
// 		writeErr(w, "no valid participants", http.StatusBadRequest)
// 		return
// 	}

// 	// Sort participants to ensure consistent matching
// 	sort.Strings(participants)

// 	// Check for existing chat with same participants
// 	filter := bson.M{
// 		"participants": bson.M{
// 			"$all":  participants,
// 			"$size": len(participants),
// 		},
// 	}

// 	if body.EntityType != "" {
// 		filter["entityType"] = body.EntityType
// 	}
// 	if body.EntityId != "" {
// 		filter["entityId"] = body.EntityId
// 	}

// 	var existing models.Chat
// 	err := db.MereCollection.FindOne(ctx, filter).Decode(&existing)
// 	if err == nil {
// 		// Existing chat found
// 		w.Header().Set("Content-Type", "application/json")
// 		_ = json.NewEncoder(w).Encode(existing)
// 		return
// 	}
// 	if err != mongo.ErrNoDocuments {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Create new chat
// 	now := time.Now()
// 	chat := models.Chat{
// 		Participants: participants,
// 		CreatedAt:    now,
// 		UpdatedAt:    now,
// 		EntityType:   body.EntityType,
// 		EntityId:     body.EntityId,
// 		ChatID:       utils.GenerateRandomString(16),
// 	}

// 	_, err = db.MereCollection.InsertOne(ctx, chat)
// 	if err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	_ = json.NewEncoder(w).Encode(chat)
// }

// func StartNewChat(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)

// 	var body struct {
// 		Participants []string `json:"participants"`
// 		EntityType   string   `json:"entityType"`
// 		EntityId     string   `json:"entityId"`
// 	}

// 	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
// 		writeErr(w, "invalid body", http.StatusBadRequest)
// 		return
// 	}

// 	if len(body.Participants) == 0 {
// 		writeErr(w, "participants required", http.StatusBadRequest)
// 		return
// 	}

// 	// Deduplicate and ensure requester included
// 	seen := make(map[string]struct{}, len(body.Participants)+1)
// 	var participants []string
// 	for _, p := range body.Participants {
// 		if p == "" {
// 			continue
// 		}
// 		if _, ok := seen[p]; ok {
// 			continue
// 		}
// 		seen[p] = struct{}{}
// 		participants = append(participants, p)
// 	}

// 	if _, ok := seen[user]; !ok {
// 		participants = append(participants, user)
// 		seen[user] = struct{}{}
// 	}

// 	if len(participants) == 0 {
// 		writeErr(w, "no valid participants", http.StatusBadRequest)
// 		return
// 	}

// 	// Exact match check: same participants (no more, no less)
// 	filter := bson.M{
// 		"participants": bson.M{
// 			"$all":  participants,
// 			"$size": len(participants),
// 		},
// 		"entityType": body.EntityType,
// 		"entityId":   body.EntityId,
// 	}

// 	var existing models.Chat
// 	err := db.MereCollection.FindOne(ctx, filter).Decode(&existing)
// 	if err == nil {
// 		// Existing chat found
// 		w.Header().Set("Content-Type", "application/json")
// 		_ = json.NewEncoder(w).Encode(existing)
// 		return
// 	}
// 	if err != mongo.ErrNoDocuments {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Create new chat
// 	now := time.Now()
// 	chat := models.Chat{
// 		Participants: participants,
// 		CreatedAt:    now,
// 		UpdatedAt:    now,
// 		EntityType:   body.EntityType,
// 		EntityId:     body.EntityId,
// 		ChatID:       utils.GenerateRandomString(16),
// 	}

// 	_, err = db.MereCollection.InsertOne(ctx, chat)
// 	if err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	w.Header().Set("Content-Type", "application/json")
// 	_ = json.NewEncoder(w).Encode(chat)
// }

func GetChatByID(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	chatID := ps.ByName("chatid")
	var chat models.Chat
	// enforce that requesting user is a participant
	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Decode(&chat); err != nil {
		if err == mongo.ErrNoDocuments {
			writeErr(w, "not found or access denied", http.StatusNotFound)
			return
		}
		writeErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(chat); err != nil {
		writeErr(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

func GetChatMessages(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()
	user := utils.GetUserIDFromRequest(r)

	chatID := strings.TrimSpace(ps.ByName("chatid"))
	if chatID == "" {
		writeErr(w, "missing chat id", http.StatusBadRequest)
		return
	}

	// verify user can access the chat
	if err := db.MereCollection.FindOne(ctx, bson.M{
		"chatid":       chatID,
		"participants": user,
	}).Err(); err != nil {
		if err == mongo.ErrNoDocuments {
			writeErr(w, "not found or access denied", http.StatusNotFound)
			return
		}
		writeErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	// pagination
	limit := int64(50)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := parseInt64(l); err == nil && v > 0 {
			limit = v
		}
	}
	skip := int64(0)
	if s := r.URL.Query().Get("skip"); s != "" {
		if v, err := parseInt64(s); err == nil && v >= 0 {
			skip = v
		}
	}

	// exclude deleted messages
	filter := bson.M{
		"chatid":  chatID, // field in messages collection
		"deleted": bson.M{"$ne": true},
	}
	opts := options.Find().SetSort(bson.M{"createdAt": 1}).SetLimit(limit).SetSkip(skip)
	cursor, err := db.MessagesCollection.Find(ctx, filter, opts)
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var msgs []models.Message
	if err := cursor.All(ctx, &msgs); err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if msgs == nil {
		msgs = make([]models.Message, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(msgs); err != nil {
		writeErr(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

// SendMessageREST handles plain text messages via HTTP
func SendMessageREST(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()

	chatID := ps.ByName("chatid")

	// verify access
	user := utils.GetUserIDFromRequest(r)
	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Err(); err != nil {
		if err == mongo.ErrNoDocuments {
			writeErr(w, "not found or access denied", http.StatusNotFound)
			return
		}
		writeErr(w, "internal error", http.StatusInternalServerError)
		return
	}

	var body struct {
		Content  string `json:"content"`
		ClientID string `json:"clientId,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeErr(w, "content required", http.StatusBadRequest)
		return
	}

	msg, err := persistMessage(ctx, chatID, user, body.Content, "", "")
	if err != nil {
		writeErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build response payload (echo back clientId if provided)
	resp := map[string]interface{}{
		"id":        msg.ID.Hex(),
		"sender":    msg.UserID,
		"content":   msg.Content,
		"createdAt": msg.CreatedAt,
		"media":     msg.Media,
		"chatid":    msg.ChatID,
	}
	if body.ClientID != "" {
		resp["clientId"] = body.ClientID
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		writeErr(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
}

// // discord/rest.go
// package discord

// import (
// 	"encoding/json"
// 	"fmt"
// 	"io"
// 	"mime/multipart"
// 	"net/http"
// 	"strings"
// 	"time"

// 	"naevis/db"
// 	"naevis/filemgr"
// 	"naevis/utils"

// 	"github.com/julienschmidt/httprouter"
// 	"go.mongodb.org/mongo-driver/bson"
// 	"go.mongodb.org/mongo-driver/mongo"
// 	"go.mongodb.org/mongo-driver/mongo/options"
// )

// // UploadAttachment handles media/file upload into a chat
// func UploadAttachment(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)

// 	chatIDHex := ps.ByName("chatid")
// 	// chatID, err := primitive.ObjectIDFromHex(chatIDHex)
// 	// if err != nil {
// 	// 	writeErr(w, "invalid chatid", http.StatusBadRequest)
// 	// 	return
// 	// }
// 	chatID := chatIDHex

// 	// Ensure user is participant of the chat
// 	var chat Chat
// 	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Decode(&chat); err != nil {
// 		if err == mongo.ErrNoDocuments {
// 			writeErr(w, "chat not found or access denied", http.StatusNotFound)
// 			return
// 		}
// 		writeErr(w, "internal error", http.StatusInternalServerError)
// 		return
// 	}

// 	// Allow up to 50MB by default; keep previous limit as fallback
// 	if err := r.ParseMultipartForm(50 << 20); err != nil {
// 		writeErr(w, "invalid form", http.StatusBadRequest)
// 		return
// 	}

// 	var header *multipart.FileHeader
// 	if r.MultipartForm != nil && r.MultipartForm.File != nil {
// 		files := r.MultipartForm.File["file"]
// 		if len(files) > 0 {
// 			header = files[0]
// 		}
// 	}
// 	if header == nil {
// 		writeErr(w, "no file provided", http.StatusBadRequest)
// 		return
// 	}

// 	// Try to determine content type more reliably by peeking into the file.
// 	contentType := header.Header.Get("Content-Type")
// 	// If header is missing or generic, sniff first 512 bytes.
// 	if contentType == "" || contentType == "application/octet-stream" {
// 		f, err := header.Open()
// 		if err == nil {
// 			defer f.Close()
// 			buf := make([]byte, 512)
// 			n, _ := io.ReadFull(f, buf)
// 			contentType = http.DetectContentType(buf[:n])
// 		}
// 		// if we couldn't open or sniff, fall back to header
// 		if contentType == "" {
// 			contentType = header.Header.Get("Content-Type")
// 		}
// 	}

// 	// Map content type → PictureType
// 	var picType filemgr.PictureType
// 	switch {
// 	case strings.HasPrefix(contentType, "image/"):
// 		picType = filemgr.PicPhoto
// 	case strings.HasPrefix(contentType, "video/"):
// 		picType = filemgr.PicVideo
// 	case strings.HasPrefix(contentType, "application/"), strings.HasPrefix(contentType, "text/"):
// 		// treat text/* as files
// 		picType = filemgr.PicFile
// 	default:
// 		writeErr(w, "unsupported file type", http.StatusBadRequest)
// 		return
// 	}

// 	// Save file via filemgr
// 	savedName, err := filemgr.SaveFormFile(r.MultipartForm, "file", filemgr.EntityChat, picType, false)
// 	if err != nil {
// 		writeErr(w, "cannot save file", http.StatusInternalServerError)
// 		return
// 	}

// 	// Persist media message
// 	msg, err := persistMediaMessage(ctx, chatID, user, savedName, contentType)
// 	if err != nil {
// 		writeErr(w, "failed to persist message", http.StatusInternalServerError)
// 		return
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	if err := json.NewEncoder(w).Encode(msg); err != nil {
// 		// encoding failed
// 		writeErr(w, "failed to encode response", http.StatusInternalServerError)
// 		return
// 	}
// }

// func GetUserChats(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)
// 	cursor, err := db.MereCollection.Find(ctx, bson.M{"participants": user})
// 	if err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	defer cursor.Close(ctx)

// 	var chats []Chat
// 	if err := cursor.All(ctx, &chats); err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	// ensure non-nil slice
// 	if chats == nil {
// 		chats = make([]Chat, 0)
// 	}
// 	w.Header().Set("Content-Type", "application/json")
// 	if err := json.NewEncoder(w).Encode(chats); err != nil {
// 		writeErr(w, "failed to encode response", http.StatusInternalServerError)
// 		return
// 	}
// }
// func StartNewChat(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)

// 	var body struct {
// 		Participants []string `json:"participants"`
// 		EntityType   string   `json:"entityType"`
// 		EntityId     string   `json:"entityId"`
// 	}

// 	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
// 		writeErr(w, "invalid body", http.StatusBadRequest)
// 		return
// 	}

// 	if len(body.Participants) == 0 {
// 		writeErr(w, "participants required", http.StatusBadRequest)
// 		return
// 	}

// 	// Deduplicate and ensure requester included
// 	seen := make(map[string]struct{}, len(body.Participants)+1)
// 	var participants []string
// 	for _, p := range body.Participants {
// 		if p == "" {
// 			continue
// 		}
// 		if _, ok := seen[p]; ok {
// 			continue
// 		}
// 		seen[p] = struct{}{}
// 		participants = append(participants, p)
// 	}

// 	if _, ok := seen[user]; !ok {
// 		participants = append(participants, user)
// 		seen[user] = struct{}{}
// 	}

// 	if len(participants) == 0 {
// 		writeErr(w, "no valid participants", http.StatusBadRequest)
// 		return
// 	}

// 	// Exact match check: same participants (no more, no less)
// 	filter := bson.M{
// 		"participants": bson.M{
// 			"$all":  participants,
// 			"$size": len(participants),
// 		},
// 		"entityType": body.EntityType,
// 		"entityId":   body.EntityId,
// 	}

// 	var existing Chat
// 	err := db.MereCollection.FindOne(ctx, filter).Decode(&existing)
// 	if err == nil {
// 		// Existing chat found
// 		w.Header().Set("Content-Type", "application/json")
// 		json.NewEncoder(w).Encode(existing)
// 		return
// 	}
// 	if err != mongo.ErrNoDocuments {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Create new chat
// 	now := time.Now()
// 	chat := Chat{
// 		Participants: participants,
// 		CreatedAt:    now,
// 		UpdatedAt:    now,
// 		EntityType:   body.EntityType,
// 		EntityId:     body.EntityId,
// 		ChatID:       utils.GenerateRandomString(16),
// 	}

// 	_, err = db.MereCollection.InsertOne(ctx, chat)
// 	if err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	w.Header().Set("Content-Type", "application/json")
// 	json.NewEncoder(w).Encode(chat)
// }

// func GetChatByID(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)

// 	chatID := ps.ByName("chatid")
// 	var chat Chat
// 	// enforce that requesting user is a participant
// 	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Decode(&chat); err != nil {
// 		if err == mongo.ErrNoDocuments {
// 			writeErr(w, "not found or access denied", http.StatusNotFound)
// 			return
// 		}
// 		writeErr(w, "internal error", http.StatusInternalServerError)
// 		return
// 	}
// 	w.Header().Set("Content-Type", "application/json")
// 	if err := json.NewEncoder(w).Encode(chat); err != nil {
// 		writeErr(w, "failed to encode response", http.StatusInternalServerError)
// 		return
// 	}
// }
// func GetChatMessages(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
// 	ctx := r.Context()
// 	user := utils.GetUserIDFromRequest(r)

// 	chatID := strings.TrimSpace(ps.ByName("chatid"))
// 	fmt.Println("chatID param:", chatID)
// 	if chatID == "" {
// 		writeErr(w, "missing chat id", http.StatusBadRequest)
// 		return
// 	}

// 	// verify user can access the chat
// 	if err := db.MereCollection.FindOne(ctx, bson.M{
// 		"chatid":       chatID,
// 		"participants": user,
// 	}).Err(); err != nil {
// 		if err == mongo.ErrNoDocuments {
// 			writeErr(w, "not found or access denied", http.StatusNotFound)
// 			return
// 		}
// 		writeErr(w, "internal error", http.StatusInternalServerError)
// 		return
// 	}

// 	// pagination
// 	limit := int64(50)
// 	if l := r.URL.Query().Get("limit"); l != "" {
// 		if v, err := parseInt64(l); err == nil && v > 0 {
// 			limit = v
// 		}
// 	}
// 	skip := int64(0)
// 	if s := r.URL.Query().Get("skip"); s != "" {
// 		if v, err := parseInt64(s); err == nil && v >= 0 {
// 			skip = v
// 		}
// 	}

// 	// exclude deleted messages
// 	filter := bson.M{
// 		"chatid":  chatID, // field in messages collection
// 		"deleted": bson.M{"$ne": true},
// 	}
// 	opts := options.Find().SetSort(bson.M{"createdAt": 1}).SetLimit(limit).SetSkip(skip)
// 	cursor, err := db.MessagesCollection.Find(ctx, filter, opts)
// 	if err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	defer cursor.Close(ctx)

// 	var msgs []Message
// 	if err := cursor.All(ctx, &msgs); err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}
// 	if msgs == nil {
// 		msgs = make([]Message, 0)
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	if err := json.NewEncoder(w).Encode(msgs); err != nil {
// 		writeErr(w, "failed to encode response", http.StatusInternalServerError)
// 		return
// 	}
// }

// // SendMessageREST handles plain text messages via HTTP
// func SendMessageREST(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
// 	ctx := r.Context()

// 	chatID := ps.ByName("chatid")

// 	// verify access
// 	user := utils.GetUserIDFromRequest(r)
// 	if err := db.MereCollection.FindOne(ctx, bson.M{"chatid": chatID, "participants": user}).Err(); err != nil {
// 		if err == mongo.ErrNoDocuments {
// 			writeErr(w, "not found or access denied", http.StatusNotFound)
// 			return
// 		}
// 		writeErr(w, "internal error", http.StatusInternalServerError)
// 		return
// 	}

// 	var body struct {
// 		Content  string `json:"content"`
// 		ClientID string `json:"clientId,omitempty"`
// 	}
// 	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
// 		writeErr(w, "invalid body", http.StatusBadRequest)
// 		return
// 	}
// 	if strings.TrimSpace(body.Content) == "" {
// 		writeErr(w, "content required", http.StatusBadRequest)
// 		return
// 	}

// 	msg, err := persistMessage(ctx, chatID, user, body.Content, "", "")
// 	if err != nil {
// 		writeErr(w, err.Error(), http.StatusInternalServerError)
// 		return
// 	}

// 	// Build response payload (echo back clientId if provided)
// 	resp := map[string]interface{}{
// 		"id":        msg.ID.Hex(),
// 		"sender":    msg.Sender,
// 		"content":   msg.Content,
// 		"createdAt": msg.CreatedAt,
// 		"media":     msg.Media,
// 	}
// 	if body.ClientID != "" {
// 		resp["clientId"] = body.ClientID
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	if err := json.NewEncoder(w).Encode(resp); err != nil {
// 		writeErr(w, "failed to encode response", http.StatusInternalServerError)
// 		return
// 	}
// }
