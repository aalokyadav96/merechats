package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"naevis/globals"
	"naevis/middleware"
)

// --- Parsing Helpers ---

func ParseFloat(s string) float64 {
	val, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return val
}

func ParseInt(s string) int {
	val, _ := strconv.Atoi(strings.TrimSpace(s))
	return val
}

func ParseDate(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &t
}

// --- File Upload Helpers ---

func SaveUploadedImage(file multipart.File, header *multipart.FileHeader) (string, error) {
	ext := filepath.Ext(header.Filename)
	filename := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	dstPath := filepath.Join("static", "uploads", "crops", filename)

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return "", err
	}
	out, err := os.Create(dstPath)
	if err != nil {
		return "", err
	}
	defer out.Close()
	_, err = io.Copy(out, file)
	return "/uploads/crops/" + filename, err
}

// --- MimeType and UUID ---

func GuessMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	default:
		return "application/octet-stream"
	}
}

func GetUUID() string {
	return uuid.New().String()
}

func SanitizeText(s string) string {
	return strings.TrimSpace(s)
}

// --- MongoDB Helpers ---

func FindAndDecode[T any](ctx context.Context, col *mongo.Collection, filter interface{}, opts ...*options.FindOptions) ([]T, error) {
	cursor, err := col.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var results []T
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}
	return results, nil
}

type QueryOptions struct {
	Page      int
	Limit     int
	Published *bool
	Search    string
	Genre     string
}

func ParseQueryOptions(r *http.Request) QueryOptions {
	q := r.URL.Query()

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 {
		limit = 10
	}

	var published *bool
	if pubStr := q.Get("published"); pubStr != "" {
		val := pubStr == "true"
		published = &val
	}

	return QueryOptions{
		Page:      page,
		Limit:     limit,
		Published: published,
		Search:    q.Get("search"),
		Genre:     q.Get("genre"),
	}
}

func ContainsIgnoreCase(str, substr string) bool {
	return strings.Contains(strings.ToLower(str), strings.ToLower(substr))
}

// --- HTTP JSON Helpers ---

func RespondWithJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func RespondWithError(w http.ResponseWriter, code int, msg string) {
	RespondWithJSON(w, code, map[string]string{"error": msg})
}

type M map[string]interface{}

func ToJSON(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}

// --- Sorting and Filtering ---

// SortListings sorts a slice of structs by a specified field (string or int) and order ("asc", "desc").
// It uses reflection to get field values dynamically.
func SortListings[T any](list []T, field, order string) {
	less := func(i, j int) bool {
		vi := reflect.ValueOf(list[i])
		vj := reflect.ValueOf(list[j])

		fi := reflect.Indirect(vi).FieldByName(field)
		fj := reflect.Indirect(vj).FieldByName(field)
		if !fi.IsValid() || !fj.IsValid() {
			return false
		}

		switch fi.Kind() {
		case reflect.String:
			if order == "asc" {
				return fi.String() < fj.String()
			}
			return fi.String() > fj.String()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if order == "asc" {
				return fi.Int() < fj.Int()
			}
			return fi.Int() > fj.Int()
		case reflect.Float32, reflect.Float64:
			if order == "asc" {
				return fi.Float() < fj.Float()
			}
			return fi.Float() > fj.Float()
		default:
			return false
		}
	}

	sort.SliceStable(list, less)
}

// RegexFilter creates a case-insensitive regex filter for MongoDB queries.
func RegexFilter(field, value string) bson.M {
	if value == "" {
		return bson.M{}
	}
	return bson.M{field: bson.M{"$regex": regexp.QuoteMeta(value), "$options": "i"}}
}

// ParseSort returns a bson.D sort specifier based on a query param and mapping.
func ParseSort(param string, defaultSort bson.D, sortMap map[string]bson.D) bson.D {
	if sort, ok := sortMap[param]; ok {
		return sort
	}
	return defaultSort
}

// ParsePagination extracts skip and limit values from the HTTP request with default and max limits.
func ParsePagination(r *http.Request, defaultLimit, maxLimit int64) (skip, limit int64) {
	page, _ := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	limit, _ = strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)

	if page < 1 {
		page = 1
	}
	if limit <= 0 || limit > maxLimit {
		limit = defaultLimit
	}

	skip = (page - 1) * limit
	return
}

// --- User Context Helpers ---

func GetUserIDFromRequest(r *http.Request) string {
	ctx := r.Context()
	userID, ok := ctx.Value(globals.UserIDKey).(string)
	if !ok || userID == "" {
		return ""
	}
	log.Println("-----|-------", userID, "---------|-------")
	return userID
}

func GetUsernameFromRequest(r *http.Request) string {
	tokenString := r.Header.Get("Authorization")
	claims, err := middleware.ValidateJWT(tokenString)
	if err != nil {
		return ""
	}
	return claims.Username
}
