package filemgr

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"naevis/mq"

	"github.com/disintegration/imaging"
)

const (
	defaultThumbWidth = 500
	maxUploadSize     = 10 << 20 // 10 MB
	defaultQuality    = 85
)

// SaveFileForEntity saves file and triggers image/video processing.
func SaveFileForEntity(file multipart.File, header *multipart.FileHeader, entity EntityType, picType PictureType) (string, error) {
	defer file.Close()

	path := ResolvePath(entity, picType)
	filename, err := SaveFile(file, header, path, maxUploadSize, nil)
	if err != nil {
		return "", err
	}

	fullPath := filepath.Join(path, filename)
	ext := strings.ToLower(filepath.Ext(fullPath))

	// Handle images
	if isImageType(picType) {
		f, err := os.Open(fullPath)
		if err != nil {
			return "", fmt.Errorf("reopen saved file: %w", err)
		}
		img, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			if LogFunc != nil {
				LogFunc(filename, 0, "unknown")
			}
			return filename, nil
		}

		// Normalize to PNG
		newPath, err := normalizeImageFormat(fullPath, ext, img)
		if err != nil {
			return "", err
		}
		if newPath != fullPath {
			fullPath = newPath
			filename = filepath.Base(newPath)
			ext = ".png"
		}

		// MQ notify
		go func(p, ent, fname string, pt string) {
			_ = mq.NotifyImageSaved(p, ent, fname, pt, "")
		}(fullPath, string(entity), filename, string(picType))

		// Thumbnail
		imgCopy := imaging.Clone(img)
		go func(img image.Image, ent EntityType, fname string) {
			if err := generateThumbnail(img, ent, fname, defaultThumbWidth); err != nil {
				if LogFunc != nil {
					LogFunc(fmt.Sprintf("warning: thumbnail failed for %s: %v", fname, err), 0, "")
				}
			}
		}(imgCopy, entity, filename)

		// Metadata extraction
		go func(img image.Image, uid string) {
			if err := ExtractImageMetadata(img, uid); err != nil {
				if LogFunc != nil {
					LogFunc(fmt.Sprintf("warning: metadata extraction failed for %s: %v", filename, err), 0, "")
				}
			}
		}(imaging.Clone(img), generateUniqueID())

		if LogFunc != nil {
			LogFunc(filename, 0, "image/png")
		}
		return filename, nil
	}

	// Handle videos
	if picType == PicVideo || isVideoExt(ext) {
		go func(vpath string, ent EntityType, fname string) {
			if thumb, err := generateVideoPoster(vpath, ent, fname); err != nil {
				if LogFunc != nil {
					LogFunc(fmt.Sprintf("warning: video poster generation failed for %s: %v", fname, err), 0, "")
				}
			} else {
				if LogFunc != nil {
					LogFunc(thumb, 0, "image/jpeg")
				}
			}
		}(fullPath, entity, filename)
	}

	if LogFunc != nil {
		LogFunc(filename, 0, "")
	}
	return filename, nil
}

// --- Utility functions for images/videos ---

// normalizeImageFormat re-encodes non-PNG images into PNG
func normalizeImageFormat(fullPath, ext string, img image.Image) (string, error) {
	if ext == ".png" {
		return fullPath, nil
	}
	pngPath := strings.TrimSuffix(fullPath, ext) + ".png"
	out, err := os.Create(pngPath)
	if err != nil {
		return fullPath, fmt.Errorf("create png %s: %w", pngPath, err)
	}
	if err := png.Encode(out, img); err != nil {
		_ = out.Close()
		_ = os.Remove(pngPath)
		return fullPath, fmt.Errorf("encode png: %w", err)
	}
	_ = out.Close()
	_ = os.Remove(fullPath)
	return pngPath, nil
}

// generateThumbnail creates a JPEG thumbnail for an image
func generateThumbnail(img image.Image, entity EntityType, baseFilename string, thumbWidth int) error {
	resized := imaging.Resize(img, thumbWidth, 0, imaging.Lanczos)
	name := strings.TrimSuffix(baseFilename, filepath.Ext(baseFilename)) + ".jpg"
	path := filepath.Join(ResolvePath(entity, PicThumb), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create thumbnail: %w", err)
	}
	if err := jpeg.Encode(out, resized, &jpeg.Options{Quality: defaultQuality}); err != nil {
		_ = out.Close()
		_ = os.Remove(path)
		return fmt.Errorf("encode thumbnail: %w", err)
	}
	_ = out.Close()
	if LogFunc != nil {
		LogFunc(path, 0, "image/jpeg")
	}
	return nil
}

// generateVideoPoster extracts a poster frame from a video
func generateVideoPoster(videoPath string, entity EntityType, baseFilename string) (string, error) {
	thumbName := strings.TrimSuffix(baseFilename, filepath.Ext(baseFilename)) + ".jpg"
	thumbDir := ResolvePath(entity, PicThumb)
	thumbPath := filepath.Join(thumbDir, thumbName)
	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", thumbDir, err)
	}

	var ts float64 = 0.5
	cmdProbe := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", videoPath)
	if out, err := cmdProbe.Output(); err == nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			if d, err := strconv.ParseFloat(s, 64); err == nil && d > 0 {
				if d >= 0.5 {
					ts = d / 2.0
				} else {
					ts = 0.0
				}
			}
		}
	}

	ss := fmt.Sprintf("%.3f", ts)
	cmd := exec.Command("ffmpeg", "-y", "-i", videoPath, "-ss", ss, "-vframes", "1", thumbPath)
	if err := cmd.Run(); err != nil {
		fallback := exec.Command("ffmpeg", "-y", "-i", videoPath, "-ss", "0", "-vframes", "1", thumbPath)
		if ferr := fallback.Run(); ferr != nil {
			return "", fmt.Errorf("ffmpeg poster generation failed (primary: %v, fallback: %v)", err, ferr)
		}
	}

	if LogFunc != nil {
		LogFunc(thumbPath, 0, "image/jpeg")
	}
	return thumbName, nil
}

func generateUniqueID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// isVideoExt checks common video extensions
func isVideoExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mov", ".mkv", ".webm", ".avi", ".flv", ".m4v":
		return true
	default:
		return false
	}
}

// package filemgr

// import (
// 	"fmt"
// 	"image"
// 	"image/jpeg"
// 	"image/png"
// 	"mime/multipart"
// 	"os"
// 	"os/exec"
// 	"path/filepath"
// 	"strconv"
// 	"strings"
// 	"time"

// 	"naevis/mq"

// 	"golang.org/x/image/webp"

// 	"github.com/disintegration/imaging"
// )

// const defaultThumbWidth = 500

// // SaveFileForEntity saves file and triggers image/video processing.
// func SaveFileForEntity(file multipart.File, header *multipart.FileHeader, entity EntityType, picType PictureType) (string, error) {
// 	defer file.Close()

// 	path := ResolvePath(entity, picType)
// 	filename, err := SaveFile(file, header, path, 10<<20, nil)
// 	if err != nil {
// 		return "", err
// 	}

// 	fullPath := filepath.Join(path, filename)
// 	ext := strings.ToLower(filepath.Ext(fullPath))

// 	if isImageType(picType) {
// 		f, err := os.Open(fullPath)
// 		if err != nil {
// 			return "", fmt.Errorf("reopen saved file: %w", err)
// 		}
// 		img, _, err := image.Decode(f)
// 		_ = f.Close()
// 		if err != nil {
// 			if LogFunc != nil {
// 				LogFunc(filename, 0, "unknown")
// 			}
// 			return filename, nil
// 		}

// 		if ext != ".png" {
// 			pngPath := strings.TrimSuffix(fullPath, ext) + ".png"
// 			out, err := os.Create(pngPath)
// 			if err != nil {
// 				return "", fmt.Errorf("create png %s: %w", pngPath, err)
// 			}
// 			if err := png.Encode(out, img); err != nil {
// 				_ = out.Close()
// 				_ = os.Remove(pngPath)
// 				return "", fmt.Errorf("encode png: %w", err)
// 			}
// 			_ = out.Close()
// 			_ = os.Remove(fullPath)
// 			fullPath = pngPath
// 			filename = filepath.Base(pngPath)
// 			ext = ".png"
// 		}

// 		go func(p, ent, fname string, pt string) {
// 			_ = mq.NotifyImageSaved(p, ent, fname, pt, "")
// 		}(fullPath, string(entity), filename, string(picType))

// 		imgCopy := imaging.Clone(img)
// 		go func(img image.Image, ent EntityType, fname string) {
// 			if err := generateThumbnail(img, ent, fname, defaultThumbWidth); err != nil {
// 				if LogFunc != nil {
// 					LogFunc(fmt.Sprintf("warning: thumbnail failed for %s: %v", fname, err), 0, "")
// 				}
// 			}
// 		}(imgCopy, entity, filename)

// 		go func(img image.Image, uid string) {
// 			if err := ExtractImageMetadata(img, uid); err != nil {
// 				if LogFunc != nil {
// 					LogFunc(fmt.Sprintf("warning: metadata extraction failed for %s: %v", filename, err), 0, "")
// 				}
// 			}
// 		}(imaging.Clone(img), generateUniqueID())

// 		if LogFunc != nil {
// 			LogFunc(filename, 0, "image/png")
// 		}
// 		return filename, nil
// 	}

// 	if picType == PicVideo || isVideoExt(ext) {
// 		go func(vpath string, ent EntityType, fname string) {
// 			if thumb, err := generateVideoPoster(vpath, ent, fname); err != nil {
// 				if LogFunc != nil {
// 					LogFunc(fmt.Sprintf("warning: video poster generation failed for %s: %v", fname, err), 0, "")
// 				}
// 			} else {
// 				if LogFunc != nil {
// 					LogFunc(thumb, 0, "image/jpeg")
// 				}
// 			}
// 		}(fullPath, entity, filename)
// 	}

// 	if LogFunc != nil {
// 		LogFunc(filename, 0, "")
// 	}
// 	return filename, nil
// }

// // --- Utility functions for images/videos ---
// // func generateThumbnail(img image.Image, entity EntityType, baseFilename string, thumbWidth int) error {
// // 	resized := imaging.Resize(img, thumbWidth, 0, imaging.Lanczos)
// // 	name := strings.TrimSuffix(baseFilename, filepath.Ext(baseFilename)) + ".jpg"
// // 	path := filepath.Join(ResolvePath(entity, PicThumb), name)
// // 	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
// // 		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
// // 	}
// // 	out, err := os.Create(path)
// // 	if err != nil {
// // 		return fmt.Errorf("create thumbnail: %w", err)
// // 	}
// // 	if err := jpeg.Encode(out, resized, &jpeg.Options{Quality: 85}); err != nil {
// // 		_ = out.Close()
// // 		_ = os.Remove(path)
// // 		return fmt.Errorf("encode thumbnail: %w", err)
// // 	}
// // 	_ = out.Close()
// // 	if LogFunc != nil {
// // 		LogFunc(path, 0, "image/jpeg")
// // 	}
// // 	return nil
// // }

// // generateThumbnail creates JPEG and WebP thumbnails
// func generateThumbnail(img image.Image, entity EntityType, baseFilename string, thumbWidth int) error {
// 	resized := imaging.Resize(img, thumbWidth, 0, imaging.Lanczos)

// 	baseName := strings.TrimSuffix(baseFilename, filepath.Ext(baseFilename))

// 	// --- JPEG Thumbnail ---
// 	jpgName := baseName + ".jpg"
// 	jpgPath := filepath.Join(ResolvePath(entity, PicThumb), jpgName)
// 	if err := os.MkdirAll(filepath.Dir(jpgPath), 0o755); err != nil {
// 		return fmt.Errorf("mkdir %s: %w", filepath.Dir(jpgPath), err)
// 	}
// 	out, err := os.Create(jpgPath)
// 	if err != nil {
// 		return fmt.Errorf("create thumbnail jpg: %w", err)
// 	}
// 	if err := jpeg.Encode(out, resized, &jpeg.Options{Quality: 85}); err != nil {
// 		_ = out.Close()
// 		_ = os.Remove(jpgPath)
// 		return fmt.Errorf("encode thumbnail jpg: %w", err)
// 	}
// 	_ = out.Close()
// 	if LogFunc != nil {
// 		LogFunc(jpgPath, 0, "image/jpeg")
// 	}

// 	// --- WebP Thumbnail ---
// 	webpName := baseName + ".webp"
// 	webpPath := filepath.Join(ResolvePath(entity, PicThumb), webpName)
// 	outWebp, err := os.Create(webpPath)
// 	if err != nil {
// 		return fmt.Errorf("create thumbnail webp: %w", err)
// 	}
// 	if err := webp.Encode(outWebp, resized, &webp.Options{Lossless: false, Quality: 85}); err != nil {
// 		_ = outWebp.Close()
// 		_ = os.Remove(webpPath)
// 		return fmt.Errorf("encode thumbnail webp: %w", err)
// 	}
// 	_ = outWebp.Close()
// 	if LogFunc != nil {
// 		LogFunc(webpPath, 0, "image/webp")
// 	}

// 	return nil
// }

// func generateVideoPoster(videoPath string, entity EntityType, baseFilename string) (string, error) {
// 	thumbName := strings.TrimSuffix(baseFilename, filepath.Ext(baseFilename)) + ".jpg"
// 	thumbDir := ResolvePath(entity, PicThumb)
// 	thumbPath := filepath.Join(thumbDir, thumbName)
// 	if err := os.MkdirAll(thumbDir, 0o755); err != nil {
// 		return "", fmt.Errorf("mkdir %s: %w", thumbDir, err)
// 	}

// 	var ts float64 = 0.5
// 	cmdProbe := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", videoPath)
// 	if out, err := cmdProbe.Output(); err == nil {
// 		s := strings.TrimSpace(string(out))
// 		if s != "" {
// 			if d, err := strconv.ParseFloat(s, 64); err == nil && d > 0 {
// 				if d >= 2.0 {
// 					ts = d / 2.0
// 				} else if d >= 0.5 {
// 					ts = d / 2.0
// 				} else {
// 					ts = 0.0
// 				}
// 			}
// 		}
// 	}

// 	ss := fmt.Sprintf("%.3f", ts)
// 	cmd := exec.Command("ffmpeg", "-y", "-i", videoPath, "-ss", ss, "-vframes", "1", thumbPath)
// 	if err := cmd.Run(); err != nil {
// 		fallback := exec.Command("ffmpeg", "-y", "-i", videoPath, "-ss", "0", "-vframes", "1", thumbPath)
// 		if ferr := fallback.Run(); ferr != nil {
// 			return "", fmt.Errorf("ffmpeg poster generation failed (primary: %v, fallback: %v)", err, ferr)
// 		}
// 	}

// 	if LogFunc != nil {
// 		LogFunc(thumbPath, 0, "image/jpeg")
// 	}
// 	return thumbName, nil
// }

// func generateUniqueID() string {
// 	return fmt.Sprintf("%d", time.Now().UnixNano())
// }

// // isVideoExt checks common video extensions
// func isVideoExt(ext string) bool {
// 	switch strings.ToLower(ext) {
// 	case ".mp4", ".mov", ".mkv", ".webm", ".avi", ".flv", ".m4v":
// 		return true
// 	default:
// 		return false
// 	}
// }
