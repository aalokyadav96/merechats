package models

import (
	"time"
)

// Event struct for MongoDB documents
type MEvent struct {
	EventID     string    `json:"eventid"`
	Title       string    `json:"title"`
	Location    string    `json:"location"`
	Category    string    `json:"category"`
	Date        time.Time `json:"date"`
	Description string    `json:"description"`
	Image       string    `json:"banner_image"`
}

// Place struct for MongoDB documents
type MPlace struct {
	PlaceID     string `json:"placeid"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Image       string `json:"banner"`
	CreatedAt   string `json:"created_at"`
}

// Index represents the incoming JSON event structure.
type Index struct {
	EntityType string `json:"entity_type"`
	Method     string `json:"method"`
	EntityId   string `json:"entity_id"`
	ItemId     string `json:"item_id"`
	ItemType   string `json:"item_type"`
}

// Result represents a single search result.
type Result struct {
	Placeid     string    `json:"placeid" bson:"placeid"`
	Eventid     string    `json:"eventid" bson:"eventid"`
	Businessid  string    `json:"businessid" bson:"businessid"`
	Userid      string    `json:"userid" bson:"userid"`
	Type        string    `json:"type" bson:"type"`
	Location    string    `json:"location" bson:"location"`
	Address     string    `json:"address" bson:"address"`
	Category    string    `json:"category" bson:"category"`
	Date        time.Time `json:"date" bson:"date"`
	Price       string    `json:"price" bson:"price"`
	Description string    `json:"description" bson:"description"`
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Contact     string    `json:"contact,omitempty"`
	Image       string    `json:"image,omitempty"`
	Link        string    `json:"link,omitempty"`
}
