package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"naevis/rdx"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type ImageEvent struct {
	LocalPath string `json:"localPath"`
	Entity    string `json:"entity"`
	FileName  string `json:"fileName"`
	PicType   string `json:"picType"`
	Userid    string `json:"userid"`
}

// Config cache so we don't keep re-reading env vars
var (
	publicBaseURL     string
	publicStripPrefix string
)

func init() {
	publicBaseURL = strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	if publicBaseURL == "" {
		publicBaseURL = "http://localhost:4000"
	}
	publicStripPrefix = filepath.ToSlash(strings.TrimRight(os.Getenv("PUBLIC_STRIP_PREFIX"), "/"))
}

// ToPublicURL converts a local path into an accessible HTTP URL.
func ToPublicURL(p string) string {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	p = filepath.ToSlash(p)

	if publicStripPrefix != "" {
		p = strings.TrimPrefix(p, publicStripPrefix)
	}

	// Use `path.Clean` for URL-safe paths (not filepath.Clean)
	return publicBaseURL + path.Clean("/"+p)
}

// NewImageEvent builds an ImageEvent with normalized URL
func NewImageEvent(localPath, entity, fileName, picType, userid string) ImageEvent {
	return ImageEvent{
		LocalPath: ToPublicURL(localPath),
		Entity:    entity,
		FileName:  fileName,
		PicType:   picType,
		Userid:    userid,
	}
}

// NotifyImageSaved publishes an ImageEvent to Redis.
func NotifyImageSaved(localPath, entity, fileName, picType, userid string) error {
	event := NewImageEvent(localPath, entity, fileName, picType, userid)

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal image event: %w", err)
	}

	if err := rdx.Conn.Publish(context.Background(), "getting-images", data).Err(); err != nil {
		return fmt.Errorf("publish to redis: %w", err)
	}

	log.Printf("[NotifyImageSaved] Published image event: %+v", event)
	return nil
}
