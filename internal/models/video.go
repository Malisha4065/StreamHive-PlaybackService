package models

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Minimal video model for read-only playback lookup.
type Video struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UploadID         string    `gorm:"uniqueIndex" json:"upload_id"`
	UserID           string    `json:"user_id"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	Tags             string    `json:"-" gorm:"type:text[]"`
	TagsList         []string  `json:"tags" gorm:"-"`
	IsPrivate        bool      `json:"is_private"`
	Category         string    `json:"category"`
	OriginalFilename string    `json:"original_filename"`
	HLSMasterURL     string    `json:"hls_master_url"`
	ThumbnailURL     string    `json:"thumbnail_url"`
	Duration         float64   `json:"duration"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// AfterFind hook to convert Tags to TagsList after database query
func (v *Video) AfterFind(tx *gorm.DB) error {
	v.TagsList = convertPostgresArrayToSlice(v.Tags)
	return nil
}

// MarshalJSON implements custom JSON marshaling for Video
func (v Video) MarshalJSON() ([]byte, error) {
	type Alias Video
	aux := &struct {
		Tags []string `json:"tags"`
		*Alias
	}{
		Tags:  v.TagsList,
		Alias: (*Alias)(&v),
	}
	// Remove the TagsList field from JSON output by setting it to nil in the alias
	aux.Alias.TagsList = nil
	return json.Marshal(aux)
}

// Helper function to convert PostgreSQL array string to Go slice
func convertPostgresArrayToSlice(pgArray string) []string {
	if pgArray == "" || pgArray == "{}" {
		return []string{}
	}

	// Remove braces and split by comma
	trimmed := strings.Trim(pgArray, "{}")
	if trimmed == "" {
		return []string{}
	}

	parts := strings.Split(trimmed, ",")
	var result []string

	for _, part := range parts {
		// Remove quotes and unescape
		cleaned := strings.Trim(part, `"`)
		cleaned = strings.ReplaceAll(cleaned, `""`, `"`)
		result = append(result, cleaned)
	}

	return result
}
