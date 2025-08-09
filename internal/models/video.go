package models

import "time"

// Minimal video model for read-only playback lookup.
type Video struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UploadID         string    `gorm:"uniqueIndex" json:"upload_id"`
	UserID           string    `json:"user_id"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	Tags             []string  `gorm:"type:text[]" json:"tags"`
	IsPrivate        bool      `json:"is_private"`
	Category         string    `json:"category"`
	OriginalFilename string    `json:"original_filename"`
	HLSMasterURL     string    `json:"hls_master_url"`
	Duration         float64   `json:"duration"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
