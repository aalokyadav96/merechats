package filemgr

import (
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"naevis/mq"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SaveFile saves a file with validation, size limit and virus scan.
// Returns the saved filename (base name).
func SaveFile(
	reader io.Reader,
	header *multipart.FileHeader,
	destDir string,
	maxSize int64,
	customNameFn func(original string) string,
) (string, error) {

	ext := strings.ToLower(filepath.Ext(header.Filename))
	picType := detectPicType(destDir)
	if picType == "" {
		return "", fmt.Errorf("unknown picture type for folder: %s", destDir)
	}

	if !isExtensionAllowed(ext, picType) {
		return "", fmt.Errorf("%w: %s for %s", ErrInvalidExtension, ext, picType)
	}

	// Peek first 512 bytes for MIME detection
	buf := make([]byte, 512)
	n, err := io.ReadFull(io.LimitReader(reader, 512), buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("read header: %w", err)
	}

	mimeType := strings.ToLower(http.DetectContentType(buf[:n]))
	if mimeType == "application/octet-stream" {
		formMime := strings.ToLower(header.Header.Get("Content-Type"))
		if formMime != "" && isMIMEAllowed(formMime, picType) {
			mimeType = formMime
		}
	}

	if !isMIMEAllowed(mimeType, picType) {
		return "", fmt.Errorf("%w: %s for %s", ErrInvalidMIME, mimeType, picType)
	}

	if !extMatchesMIME(ext, mimeType, picType) {
		return "", fmt.Errorf("extension %s does not match MIME type %s for %s", ext, mimeType, picType)
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	filename := getSafeFilename(header.Filename, ext, customNameFn)
	fullPath := filepath.Join(destDir, filename)

	out, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", fullPath, err)
	}
	defer out.Close()

	// write initial bytes we already peeked
	if _, err := out.Write(buf[:n]); err != nil {
		return "", fmt.Errorf("write header: %w", err)
	}

	written, err := io.Copy(out, io.LimitReader(reader, maxSize-int64(n)))
	if err != nil {
		return "", fmt.Errorf("write body: %w", err)
	}

	totalWritten := written + int64(n)
	if maxSize > 0 && totalWritten > maxSize {
		_ = os.Remove(fullPath)
		return "", ErrFileTooLarge
	}

	// Virus scan after full file present
	if err := ScanForViruses(fullPath); err != nil {
		_ = os.Remove(fullPath)
		return "", fmt.Errorf("virus scan failed: %w", err)
	}

	// Log via LogFunc if present
	if LogFunc != nil {
		LogFunc(filename, totalWritten, mimeType)
	}

	return filename, nil
}

// Convenience functions for saving form files
func SaveFormFile(r *multipart.Form, formKey string, entity EntityType, picType PictureType, required bool) (string, error) {
	files := r.File[formKey]
	if len(files) == 0 {
		if required {
			return "", fmt.Errorf("missing required file: %s", formKey)
		}
		return "", nil
	}
	file, err := files[0].Open()
	if err != nil {
		return "", fmt.Errorf("open %s: %w", formKey, err)
	}
	return SaveFileForEntity(file, files[0], entity, picType)
}

func SaveFormFiles(form *multipart.Form, formKey string, entity EntityType, picType PictureType, required bool) ([]string, error) {
	files := form.File[formKey]
	if len(files) == 0 {
		if required {
			return nil, fmt.Errorf("missing required files: %s", formKey)
		}
		return nil, nil
	}
	var saved []string
	var errs []string
	for _, hdr := range files {
		file, err := hdr.Open()
		if err != nil {
			errs = append(errs, fmt.Sprintf("open %s: %v", hdr.Filename, err))
			continue
		}
		name, err := SaveFileForEntity(file, hdr, entity, picType)
		if err != nil {
			errs = append(errs, fmt.Sprintf("save %s: %v", hdr.Filename, err))
			continue
		}
		saved = append(saved, name)
	}
	if len(errs) > 0 {
		return saved, fmt.Errorf("errors saving files: %s", strings.Join(errs, "; "))
	}
	return saved, nil
}

func SaveFormFilesByKeys(form *multipart.Form, keys []string, entityType EntityType, pictureType PictureType, required bool) ([]string, error) {
	var urls []string
	var errs []string
	for _, key := range keys {
		partial, err := SaveFormFiles(form, key, entityType, pictureType, required)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", key, err))
		}
		urls = append(urls, partial...)
	}
	if len(errs) > 0 {
		return urls, fmt.Errorf("errors: %s", strings.Join(errs, "; "))
	}
	return urls, nil
}

// SaveImageWithThumb saves an image, validates dimensions and creates a thumbnail; returns image name and thumbnail name (if created).
func SaveImageWithThumb(file multipart.File, header *multipart.FileHeader, entity EntityType, picType PictureType, thumbWidth int, userid string) (string, string, error) {
	defer file.Close()

	origPath := ResolvePath(entity, picType)
	origName, err := SaveFile(file, header, origPath, maxUploadSize, nil)
	if err != nil {
		return "", "", fmt.Errorf("save original: %w", err)
	}

	fullPath := filepath.Join(origPath, origName)

	f, err := os.Open(fullPath)
	if err != nil {
		return origName, "", fmt.Errorf("open for decode: %w", err)
	}
	img, _, err := image.Decode(f)
	_ = f.Close()
	if err != nil {
		return origName, "", fmt.Errorf("decode %q: %w", header.Filename, err)
	}

	// Normalize to PNG
	ext := strings.ToLower(filepath.Ext(fullPath))
	newPath, err := normalizeImageFormat(fullPath, ext, img)
	if err != nil {
		return origName, "", err
	}
	if newPath != fullPath {
		fullPath = newPath
		origName = filepath.Base(newPath)
	}

	if err := ValidateImageDimensions(img, 3000, 3000); err != nil {
		return origName, "", fmt.Errorf("invalid image %q: %w", header.Filename, err)
	}

	// Notify MQ (best-effort)
	go func(p, ent, name, pt, uid string) {
		_ = mq.NotifyImageSaved(p, ent, name, pt, uid)
	}(fullPath, string(entity), origName, string(picType), userid)

	// Thumbnail creation (JPEG only)
	if img.Bounds().Dx() > thumbWidth || img.Bounds().Dy() > thumbWidth {
		thumbName := userid + ".jpg"
		if err := generateThumbnail(img, entity, thumbName, thumbWidth); err != nil {
			return origName, "", fmt.Errorf("thumbnail failed: %w", err)
		}
		return origName, thumbName, nil
	}

	if LogFunc != nil {
		LogFunc(origName, 0, "image/png")
	}
	return origName, "", nil
}
