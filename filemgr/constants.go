package filemgr

import "errors"

type EntityType string
type PictureType string

const (
	EntityArtist  EntityType = "artist"
	EntityUser    EntityType = "user"
	EntityBaito   EntityType = "baito"
	EntityWorker  EntityType = "worker"
	EntitySong    EntityType = "song"
	EntityPost    EntityType = "post"
	EntityChat    EntityType = "chat"
	EntityEvent   EntityType = "event"
	EntityFarm    EntityType = "farm"
	EntityCrop    EntityType = "crop"
	EntityPlace   EntityType = "place"
	EntityMedia   EntityType = "media"
	EntityFeed    EntityType = "feed"
	EntityProduct EntityType = "product"

	PicBanner   PictureType = "banner"
	PicPhoto    PictureType = "photo"
	PicPoster   PictureType = "poster"
	PicSeating  PictureType = "seating"
	PicMember   PictureType = "member"
	PicThumb    PictureType = "thumb"
	PicAudio    PictureType = "audio"
	PicVideo    PictureType = "video"
	PicDocument PictureType = "document"
	PicFile     PictureType = "file"
)

var (
	AllowedExtensions = map[PictureType][]string{
		PicPhoto:    {".jpg", ".jpeg", ".png", ".gif", ".webp"},
		PicThumb:    {".jpg"},
		PicPoster:   {".jpg", ".jpeg", ".png", ".webp"},
		PicBanner:   {".jpg", ".jpeg", ".png", ".webp"},
		PicMember:   {".jpg", ".jpeg", ".png", ".webp"},
		PicSeating:  {".jpg", ".jpeg", ".png", ".webp"},
		PicAudio:    {".mp3", ".wav", ".aac"},
		PicVideo:    {".mp4", ".webm"},
		PicDocument: {".pdf"},
		PicFile:     {".pdf", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".mp3", ".mp4", ".webm"},
	}

	AllowedMIMEs = map[PictureType][]string{
		PicPhoto:   {"image/jpeg", "image/png", "image/gif", "image/webp"},
		PicThumb:   {"image/jpeg"},
		PicPoster:  {"image/jpeg", "image/png", "image/webp"},
		PicBanner:  {"image/jpeg", "image/png", "image/webp"},
		PicMember:  {"image/jpeg", "image/png", "image/webp"},
		PicSeating: {"image/jpeg", "image/png", "image/webp"},
		PicAudio:   {"audio/mpeg", "audio/wav", "audio/aac", "video/mp4"},
		PicVideo:   {"video/mp4", "video/webm"},
		PicDocument: {
			"application/pdf",
		},
		PicFile: {
			"application/pdf",
			"image/jpeg", "image/png", "image/gif", "image/webp",
			"audio/mpeg", "audio/wav",
			"video/mp4", "video/webm",
		},
	}

	PictureSubfolders = map[PictureType]string{
		PicBanner:   "banner",
		PicPhoto:    "photo",
		PicPoster:   "poster",
		PicSeating:  "seating",
		PicMember:   "member",
		PicThumb:    "thumb",
		PicAudio:    "audio",
		PicVideo:    "videos",
		PicDocument: "docs",
		PicFile:     "files",
	}

	ErrInvalidExtension = errors.New("invalid file extension")
	ErrInvalidMIME      = errors.New("invalid MIME type")
	ErrFileTooLarge     = errors.New("file size exceeds limit")

	LogFunc func(path string, size int64, mimeType string)
)
