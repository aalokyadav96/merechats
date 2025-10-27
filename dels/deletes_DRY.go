package dels

import (
	"context"
	"fmt"
	"naevis/db"
	"naevis/globals"
	"naevis/middleware"
	"naevis/models"
	"naevis/mq"
	"naevis/rdx"
	"naevis/utils"
	"net/http"
	"time"

	"github.com/julienschmidt/httprouter"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type permissionFn func(ctx context.Context, r *http.Request, entityID string) error
type afterDeleteFn func(ctx context.Context, entityID, userID string)

// ---- Core Helper ----

func deleteByField(
	w http.ResponseWriter, r *http.Request, ps httprouter.Params,
	collection *mongo.Collection, paramKey, fieldKey, entityType, mqTopic string,
	perm permissionFn, after afterDeleteFn,
) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	entityID := ps.ByName(paramKey)
	if entityID == "" {
		http.Error(w, "Missing ID", http.StatusBadRequest)
		return
	}

	userID, _ := r.Context().Value(globals.UserIDKey).(string)

	if perm != nil {
		if err := perm(ctx, r, entityID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	res, err := collection.DeleteOne(ctx, bson.M{fieldKey: entityID})
	if err != nil || res.DeletedCount == 0 {
		http.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}

	if after != nil {
		after(ctx, entityID, userID)
	}

	go mq.Emit(ctx, mqTopic, models.Index{EntityType: entityType, EntityId: entityID, Method: "DELETE"})

	utils.RespondWithJSON(w, http.StatusOK, utils.M{"success": true})
}

// ---- Soft Delete Helper ----

func softDeleteByField(
	w http.ResponseWriter, r *http.Request, ps httprouter.Params,
	collection *mongo.Collection, paramKey, fieldKey, entityType, mqTopic string,
	update bson.M, perm permissionFn, after afterDeleteFn,
) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	entityID := ps.ByName(paramKey)
	if entityID == "" {
		http.Error(w, "Missing ID", http.StatusBadRequest)
		return
	}

	userID, _ := r.Context().Value(globals.UserIDKey).(string)

	if perm != nil {
		if err := perm(ctx, r, entityID); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	_, err := collection.UpdateOne(ctx, bson.M{fieldKey: entityID}, update)
	if err != nil {
		http.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}

	if after != nil {
		after(ctx, entityID, userID)
	}

	go mq.Emit(ctx, mqTopic, models.Index{EntityType: entityType, EntityId: entityID, Method: "DELETE"})

	utils.RespondWithJSON(w, http.StatusOK, utils.M{"success": true})
}

// ---- Handlers ----

func DeleteMessage(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	softDeleteByField(w, r, ps, db.MessagesCollection, "messageId", "_id", "message", "message-deleted",
		bson.M{"$set": bson.M{"deleted": true}}, nil, nil)
}

func DeletesMessage(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	deleteByField(w, r, ps, db.MessagesCollection, "msgid", "_id", "message", "message-deleted",
		func(ctx context.Context, r *http.Request, entityID string) error {
			claims, err := middleware.ValidateJWT(r.Header.Get("Authorization"))
			if err != nil {
				return fmt.Errorf("unauthorized")
			}
			objID, _ := primitive.ObjectIDFromHex(entityID)
			var msg models.Message
			if err := db.MessagesCollection.FindOne(ctx, bson.M{"_id": objID}).Decode(&msg); err != nil {
				return fmt.Errorf("not found")
			}
			if msg.UserID != claims.UserID {
				return fmt.Errorf("forbidden")
			}
			_, _ = db.ChatsCollection.UpdateOne(ctx, bson.M{"chatid": msg.ChatID}, bson.M{"$set": bson.M{"updatedAt": time.Now()}})
			return nil
		}, nil)
}

func InvalidateCachedProfile(username string) error {
	_, err := rdx.RdxDel("profile:" + username)
	return err
}
