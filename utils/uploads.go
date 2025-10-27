package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/julienschmidt/httprouter"
)

func UploadImages(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	err := r.ParseMultipartForm(10 << 20) // 10MB max
	if err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["images"]
	if len(files) == 0 {
		http.Error(w, "No files uploaded", http.StatusBadRequest)
		return
	}

	var savedPaths []string
	for _, fileHeader := range files {
		file, err := fileHeader.Open()
		if err != nil {
			http.Error(w, "Error reading file", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		ext := filepath.Ext(fileHeader.Filename)
		filename := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
		path := filepath.Join("public/uploads", filename)
		out, err := os.Create(path)
		if err != nil {
			http.Error(w, "Error saving file", http.StatusInternalServerError)
			return
		}
		defer out.Close()

		if _, err := io.Copy(out, file); err != nil {
			http.Error(w, "Error writing file", http.StatusInternalServerError)
			return
		}

		publicPath := "/uploads/" + filename
		savedPaths = append(savedPaths, publicPath)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string][]string{
		"imageUrls": savedPaths,
	})
}
