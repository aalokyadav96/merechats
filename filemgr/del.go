package filemgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DeleteFile deletes a saved file and its thumbnail (if exists)
func DeleteFile(filePath string) error {
	if filePath == "" {
		return nil
	}
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete %s: %w", filePath, err)
	}

	// Delete thumbnail if exists
	dir := filepath.Dir(filePath)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	thumbPath := filepath.Join(dir, base+".jpg")
	if _, err := os.Stat(thumbPath); err == nil {
		_ = os.Remove(thumbPath)
	}
	return nil
}
