package filemgr

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const (
	// simple heuristic limits for our basic scanner
	virusScanReadLimit = 1 << 20 // 1 MiB
	maxAllowedSizeScan = 1 << 30 // 1 GiB, used only as a safety-check in scan
)

// ScanForViruses performs a small, fast, best-effort scan of the file at filePath.
// This is NOT a replacement for a real AV scan; it looks for common suspicious signatures
// (executable headers, inline HTML/JS in uploads, obvious "virus" markers). It returns
// an error when something suspicious is found.
func ScanForViruses(filePath string) error {
	// quick name-based check (legacy behaviour preserved)
	if strings.Contains(strings.ToLower(filePath), "virus") {
		return fmt.Errorf("virus signature matched in filename")
	}

	f, err := os.Open(filePath)
	if err != nil {
		// If we can't open the file, treat as suspicious to be safe
		return fmt.Errorf("scan: open failed: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err == nil {
		if stat.Size() <= 0 || stat.Size() > maxAllowedSizeScan {
			return fmt.Errorf("scan: suspicious file size: %d", stat.Size())
		}
	}

	// read a limited prefix
	buf := make([]byte, virusScanReadLimit)
	n, _ := io.ReadFull(f, buf)
	if n > 0 {
		prefix := strings.ToLower(string(buf[:n]))

		// Common executable headers
		if strings.HasPrefix(prefix, "mzb") || strings.HasPrefix(prefix, "mz") || strings.HasPrefix(prefix, "pe") {
			// "MZ" executable header or other binary markers
			return fmt.Errorf("scan: executable header detected")
		}

		// PKZip / docx / jar — sometimes used to smuggle executables. We don't block archives outright,
		// but if the upload path should be images only, ext/MIME checks will catch it earlier.
		if strings.HasPrefix(prefix, "pk") {
			return fmt.Errorf("scan: archive/zip signature detected")
		}

		// Basic HTML/JS injection in uploads
		if strings.Contains(prefix, "<script") || strings.Contains(prefix, "<!doctype html") || strings.Contains(prefix, "<html") {
			return fmt.Errorf("scan: html/javascript content detected")
		}

		// suspicious strings (heuristic)
		if strings.Contains(prefix, "eval(") && strings.Contains(prefix, "document") {
			return fmt.Errorf("scan: suspicious javascript-like content")
		}
	}

	// Best-effort: no issues found
	return nil
}

// StripEXIF re-encodes an image.Image into JPEG and returns the bytes buffer.
// Because standard image.Decode drops EXIF, this function is a convenient way to
// obtain image bytes without EXIF metadata. Quality defaults to 90.
func StripEXIF(img image.Image) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	if err := jpeg.Encode(buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("strip exif: encode failed: %w", err)
	}
	return buf, nil
}

// ExtractImageMetadata extracts basic metadata (width, height and an approximate byte-size)
// from the provided image and persists or logs it. The uid parameter should be a stable
// identifier for the image (filename, DB id, or generated unique id).
//
// This function intentionally keeps behaviour simple and synchronous; callers may run it in a goroutine.
func ExtractImageMetadata(img image.Image, uid string) error {
	if img == nil {
		return fmt.Errorf("extract metadata: nil image")
	}

	b := img.Bounds()
	width := b.Dx()
	height := b.Dy()

	// approximate byte-size by encoding to JPEG in-memory (best-effort)
	buf, err := StripEXIF(img)
	if err != nil {
		// don't fail hard on metadata extraction; return a warning error
		return fmt.Errorf("extract metadata: encoding failed: %w", err)
	}
	size := buf.Len()

	// Here we simply print metadata. In real application you should persist it to DB or notify a service.
	// Use fmt.Printf for compatibility with existing code paths; replace with structured logging if available.
	fmt.Printf("metadata uid=%s width=%d height=%d size=%d bytes\n", uid, width, height, size)
	return nil
}

// detectPicType attempts to infer the PictureType from a destination directory path.
// It compares the final path element against PictureSubfolders map values.
func detectPicType(destDir string) PictureType {
	clean := filepath.Clean(destDir)
	last := strings.ToLower(filepath.Base(clean))
	if last == "." || last == string(os.PathSeparator) {
		return ""
	}
	for picType, folder := range PictureSubfolders {
		if strings.ToLower(folder) == last {
			return picType
		}
	}
	return ""
}

// ensureSafeFilename sanitizes a base name (without ext) to a safe filename, returns name+ext.
// It removes unsafe chars, lowercases and collapses whitespace. If result is empty, a uuid is used.
func ensureSafeFilename(name, ext string) string {
	// strip extension if included
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	// replace whitespace with underscore
	name = strings.ReplaceAll(name, " ", "_")
	// keep alnum, underscore, dash
	reg := regexp.MustCompile(`[^a-z0-9_\-]`)
	name = reg.ReplaceAllString(name, "")
	if name == "" {
		name = uuid.New().String()
	}
	// ensure ext starts with dot and is lowercase
	if ext == "" {
		ext = ""
	} else if !strings.HasPrefix(ext, ".") {
		ext = "." + strings.ToLower(strings.TrimPrefix(ext, "."))
	} else {
		ext = strings.ToLower(ext)
	}
	return name + ext
}

// isExtensionAllowed checks if the file extension is allowed for a given picture type
func isExtensionAllowed(ext string, picType PictureType) bool {
	ext = strings.ToLower(ext)
	allowed := AllowedExtensions[picType]
	for _, a := range allowed {
		if ext == a {
			return true
		}
	}
	return false
}

// isMIMEAllowed checks if the MIME type is allowed for a given picture type
func isMIMEAllowed(mimeType string, picType PictureType) bool {
	mimeType = strings.ToLower(mimeType)
	allowed := AllowedMIMEs[picType]
	for _, a := range allowed {
		if mimeType == a {
			return true
		}
	}
	return false
}

// extMatchesMIME ensures extension and MIME are both in allowed lists for this picture type
func extMatchesMIME(ext, mimeType string, picType PictureType) bool {
	return isExtensionAllowed(ext, picType) && isMIMEAllowed(mimeType, picType)
}

// ResolvePath returns a clean uploads path for given entity and picture type.
func ResolvePath(entity EntityType, picType PictureType) string {
	subfolder := PictureSubfolders[picType]
	if subfolder == "" {
		subfolder = "misc"
	}
	// ensure lowercase and cleaned path
	return filepath.Join("static", "uploads", strings.ToLower(string(entity)), strings.ToLower(subfolder))
}

// isImageType returns true for picture types that are images.
func isImageType(picType PictureType) bool {
	switch picType {
	case PicBanner, PicPhoto, PicMember, PicPoster, PicSeating, PicThumb:
		return true
	default:
		return false
	}
}

// ValidateImageDimensions checks image dimensions against limits.
func ValidateImageDimensions(img image.Image, maxWidth, maxHeight int) error {
	if img == nil {
		return fmt.Errorf("validate dimensions: nil image")
	}
	bounds := img.Bounds()
	if bounds.Dx() > maxWidth || bounds.Dy() > maxHeight {
		return fmt.Errorf("image dimensions %dx%d exceed max %dx%d", bounds.Dx(), bounds.Dy(), maxWidth, maxHeight)
	}
	return nil
}

// getSafeFilename generates a safe filename using custom function or uuid fallback.
// The returned string includes the extension.
func getSafeFilename(original, ext string, fn func(string) string) string {
	name := ""
	if fn != nil {
		name = strings.TrimSpace(fn(original))
	}
	if name == "" {
		// keep uuid + ext
		return uuid.New().String() + ext
	}
	return ensureSafeFilename(name, ext)
}
