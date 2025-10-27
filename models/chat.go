package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// IncomingWSMessage represents a generic WebSocket inbound payload
type IncomingWSMessage struct {
	Type      string `json:"type"`
	ChatID    string `json:"chatid"`
	Content   string `json:"content"`
	MediaURL  string `json:"mediaUrl"`
	MediaType string `json:"mediaType"`
	Online    bool   `json:"online"`
	ClientID  string `json:"clientId,omitempty"`
}

// Chat represents a chat document
type Chat struct {
	ChatID       string    `bson:"chatid,omitempty" json:"chatid"`
	Participants []string  `bson:"participants"      json:"participants"`
	CreatedAt    time.Time `bson:"createdAt"         json:"createdAt"`
	UpdatedAt    time.Time `bson:"updatedAt"         json:"updatedAt"`
	EntityType   string    `bson:"entitytype"        json:"entitytype"`
	EntityId     string    `bson:"entityid"          json:"entityid"`
}

// Media represents media attached to a message
type Media struct {
	URL  string `bson:"url"  json:"url"`
	Type string `bson:"type" json:"type"`
}

// Message represents a chat message
type Message struct {
	ID         primitive.ObjectID `bson:"_id,omitempty"        json:"messageid"`
	ChatID     string             `bson:"chatid"              json:"chatid"`
	UserID     string             `bson:"sender"              json:"sender"`
	SenderName string             `bson:"senderName,omitempty" json:"senderName,omitempty"`
	AvatarURL  string             `bson:"avatarUrl,omitempty"   json:"avatarUrl,omitempty"`

	Content string              `bson:"content"           json:"content"`
	Media   *Media              `bson:"media,omitempty"   json:"media,omitempty"`
	ReplyTo *primitive.ObjectID `bson:"replyTo,omitempty" json:"replyTo,omitempty"`

	CreatedAt time.Time  `bson:"createdAt"         json:"createdAt"`
	EditedAt  *time.Time `bson:"editedAt,omitempty" json:"editedAt,omitempty"`
	Deleted   bool       `bson:"deleted"           json:"deleted"`
	ReadBy    []string   `bson:"readBy,omitempty"  json:"readBy,omitempty"`
	Status    string     `bson:"status,omitempty"  json:"status,omitempty"` // e.g. "sent", "read"
}
