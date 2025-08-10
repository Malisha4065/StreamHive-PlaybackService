package playback

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/streamhive/playback-service/internal/cache"
	"github.com/streamhive/playback-service/internal/models"
)

type Handler struct {
	db              *gorm.DB
	log             *zap.SugaredLogger
	client          *http.Client
	containerClient *container.Client
	account         string
	containerName   string
	cache           *cache.CacheService
}

func NewHandler(db *gorm.DB, log *zap.SugaredLogger) *Handler {
	// Initialize Azure client (supports connection string or account+key)
	ctx := context.Background()
	var cc *container.Client
	account := os.Getenv("AZURE_STORAGE_ACCOUNT")
	containerName := os.Getenv("AZURE_BLOB_CONTAINER")
	if conn := os.Getenv("AZURE_STORAGE_CONNECTION_STRING"); conn != "" && containerName != "" {
		c, err := container.NewClientFromConnectionString(conn, containerName, nil)
		if err == nil {
			cc = c
		} else {
			log.Errorw("azure conn string", "err", err)
		}
	} else if account != "" && containerName != "" {
		cred, err := azblob.NewSharedKeyCredential(account, os.Getenv("AZURE_STORAGE_KEY"))
		if err == nil {
			url := "https://" + account + ".blob.core.windows.net/" + containerName
			c, e2 := container.NewClientWithSharedKeyCredential(url, cred, nil)
			if e2 == nil {
				cc = c
			} else {
				log.Errorw("azure client", "err", e2)
			}
		} else {
			log.Errorw("azure credential", "err", err)
		}
	}
	if cc == nil {
		log.Warn("azure container client not initialized; playback will fail for private blobs")
	}

	// Initialize cache service
	cacheService, err := cache.NewCacheService(log)
	if err != nil {
		log.Errorw("failed to initialize cache service", "err", err)
		cacheService = nil // Continue without cache
	}

	_ = ctx // reserved
	return &Handler{
		db:              db,
		log:             log,
		client:          &http.Client{},
		containerClient: cc,
		account:         account,
		containerName:   containerName,
		cache:           cacheService,
	}
}

// GET /playback/videos/:uploadId
func (h *Handler) GetDescriptor(c *gin.Context) {
	uploadID := c.Param("uploadId")
	var v models.Video
	if err := h.db.Where("upload_id = ?", uploadID).First(&v).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"uploadId":    v.UploadID,
		"title":       v.Title,
		"description": v.Description,
		"tags":        v.Tags,
		"category":    v.Category,
		"duration":    v.Duration,
		"hls": gin.H{
			"master": c.FullPath() + "/master.m3u8", // will rewrite below
		},
	})
}

// Proxy master playlist; rewrite variant URIs to proxy endpoints.
func (h *Handler) GetMaster(c *gin.Context) {
	uploadID := c.Param("uploadId")
	var v models.Video
	if err := h.db.Where("upload_id = ?", uploadID).First(&v).Error; err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if v.HLSMasterURL == "" {
		c.String(http.StatusBadRequest, "master not ready")
		return
	}
	if h.containerClient == nil {
		// fallback to original HTTP (likely public) path
		resp, err := h.client.Get(v.HLSMasterURL)
		if err != nil {
			h.log.Errorw("fetch master", "err", err)
			c.String(http.StatusBadGateway, "upstream error")
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		// Naive rewrite of rendition lines (<res>/index.m3u8)
		re := regexp.MustCompile(`(?m)^(1080p|720p|480p|360p)/index.m3u8$`)
		rewritten := re.ReplaceAllStringFunc(string(b), func(s string) string {
			parts := strings.Split(s, "/")
			return path.Join(parts[0], "index.m3u8")
		})
		c.Header("Content-Type", "application/vnd.apple.mpegurl")
		c.String(http.StatusOK, rewritten)
		return
	}
	// Private: fetch blob path derived from stored URL
	blobPath := strings.TrimPrefix(strings.SplitN(v.HLSMasterURL, ".blob.core.windows.net/", 2)[1], h.containerName+"/")
	data, err := h.downloadBlob(c, blobPath)
	if err != nil {
		h.log.Errorw("master download", "err", err)
		c.String(http.StatusBadGateway, "blob error")
		return
	}
	// Naive rewrite of rendition lines (<res>/index.m3u8)
	re := regexp.MustCompile(`(?m)^(1080p|720p|480p|360p)/index.m3u8$`)
	rewritten := re.ReplaceAllStringFunc(string(data), func(s string) string {
		parts := strings.Split(s, "/")
		return path.Join(parts[0], "index.m3u8")
	})
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.String(http.StatusOK, rewritten)
}

// Variant playlist
func (h *Handler) GetVariant(c *gin.Context) {
	uploadID := c.Param("uploadId")
	rendition := c.Param("rendition")
	if !allowedRendition(rendition) {
		c.String(http.StatusBadRequest, "invalid rendition")
		return
	}
	var v models.Video
	if err := h.db.Where("upload_id = ?", uploadID).First(&v).Error; err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if h.containerClient != nil { // private blob path
		base := h.blobBase(v.HLSMasterURL)
		blobPath := base + "/" + rendition + "/index.m3u8"
		data, err := h.downloadBlob(c, blobPath)
		if err != nil {
			h.log.Errorw("variant download", "err", err)
			c.String(http.StatusBadGateway, "blob error")
			return
		}
		c.Header("Content-Type", "application/vnd.apple.mpegurl")
		c.String(http.StatusOK, string(data))
		return
	}
	base := baseHLSPath(v.HLSMasterURL)
	url := base + "/" + rendition + "/index.m3u8"
	proxyM3U8(c, h.client, url)
}

// Segment
func (h *Handler) GetSegment(c *gin.Context) {
	uploadID := c.Param("uploadId")
	rendition := c.Param("rendition")
	segment := c.Param("segment")
	if !allowedRendition(rendition) || !strings.HasSuffix(segment, ".ts") && !strings.HasSuffix(segment, ".m4s") {
		c.String(http.StatusBadRequest, "invalid segment")
		return
	}
	var v models.Video
	if err := h.db.Where("upload_id = ?", uploadID).First(&v).Error; err != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if h.containerClient != nil { // private
		base := h.blobBase(v.HLSMasterURL)
		blobPath := base + "/" + rendition + "/" + segment
		
		// Try cache first
		var data []byte
		var err error
		
		if h.cache != nil {
			cacheKey := h.cache.GenerateKey("segment", uploadID, blobPath)
			data, err = h.cache.Get(c.Request.Context(), cacheKey)
			if err != nil {
				h.log.Warnw("cache get error", "err", err)
			}
		}
		
		// Cache miss or no cache - fetch from Azure
		if data == nil {
			data, err = h.downloadBlob(c, blobPath)
			if err != nil {
				h.log.Errorw("segment download", "err", err)
				c.String(http.StatusBadGateway, "blob error")
				return
			}
			
			// Store in cache if available
			if h.cache != nil && data != nil {
				cacheKey := h.cache.GenerateKey("segment", uploadID, blobPath)
				if err := h.cache.Set(c.Request.Context(), cacheKey, data); err != nil {
					h.log.Warnw("cache set error", "err", err)
				}
			}
		}
		
		// Basic content-type guess
		if strings.HasSuffix(segment, ".m3u8") {
			c.Header("Content-Type", "application/vnd.apple.mpegurl")
		} else {
			c.Header("Content-Type", "video/MP2T")
		}
		c.Header("Cache-Control", "public, max-age=60")
		c.Data(http.StatusOK, c.Writer.Header().Get("Content-Type"), data)
		return
	}
	base := baseHLSPath(v.HLSMasterURL)
	url := base + "/" + rendition + "/" + segment
	proxyBinary(c, h.client, url)
}

// GetThumbnail serves video thumbnails
func (h *Handler) GetThumbnail(c *gin.Context) {
	uploadID := c.Param("uploadId")
	var v models.Video
	if err := h.db.Where("upload_id = ?", uploadID).First(&v).Error; err != nil {
		c.String(http.StatusNotFound, "Video not found")
		return
	}

	if v.ThumbnailURL == "" {
		c.String(http.StatusNotFound, "Thumbnail not available")
		return
	}

	if h.containerClient != nil {
		// Private blob: serve from Azure storage with caching
		thumbnailPath := fmt.Sprintf("thumbnails/%s/%s.jpg", v.UserID, v.UploadID)
		
		// Try cache first
		var data []byte
		var err error
		
		if h.cache != nil {
			cacheKey := h.cache.GenerateKey("thumbnail", uploadID, thumbnailPath)
			data, err = h.cache.Get(c.Request.Context(), cacheKey)
			if err != nil {
				h.log.Warnw("cache get error", "err", err)
			}
		}
		
		// Cache miss or no cache - fetch from Azure
		if data == nil {
			data, err = h.downloadBlob(c, thumbnailPath)
			if err != nil {
				h.log.Errorw("thumbnail download", "err", err)
				c.String(http.StatusNotFound, "Thumbnail not found")
				return
			}
			
			// Store in cache if available
			if h.cache != nil && data != nil {
				cacheKey := h.cache.GenerateKey("thumbnail", uploadID, thumbnailPath)
				if err := h.cache.Set(c.Request.Context(), cacheKey, data); err != nil {
					h.log.Warnw("cache set error", "err", err)
				}
			}
		}
		
		c.Header("Content-Type", "image/jpeg")
		c.Header("Cache-Control", "public, max-age=3600")
		c.Data(http.StatusOK, "image/jpeg", data)
		return
	}

	// Public blob: redirect to direct URL
	c.Redirect(http.StatusFound, v.ThumbnailURL)
}

func allowedRendition(r string) bool {
	switch r {
	case "1080p", "720p", "480p", "360p":
		return true
	}
	return false
}

func baseHLSPath(master string) string {
	// master URL ends with master.m3u8; strip
	return strings.TrimSuffix(master, "/master.m3u8")
}

func proxyM3U8(c *gin.Context, cl *http.Client, url string) {
	resp, err := cl.Get(url)
	if err != nil {
		c.String(http.StatusBadGateway, "upstream error")
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.String(resp.StatusCode, string(b))
}

func proxyBinary(c *gin.Context, cl *http.Client, url string) {
	resp, err := cl.Get(url)
	if err != nil {
		c.String(http.StatusBadGateway, "upstream error")
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if len(v) > 0 {
			c.Header(k, v[0])
		}
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}

// Optional simple readiness endpoint
func (h *Handler) Ready(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) }

// Debug config
func (h *Handler) Config(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"env": os.Environ()}) }

func (h *Handler) downloadBlob(c *gin.Context, path string) ([]byte, error) {
	ctx := c.Request.Context()
	bc := h.containerClient.NewBlobClient(path)
	resp, err := bc.DownloadStream(ctx, nil)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Helper to compute base path inside container (without container prefix and without master.m3u8)
func (h *Handler) blobBase(masterURL string) string {
	p := strings.TrimPrefix(strings.SplitN(masterURL, ".blob.core.windows.net/", 2)[1], h.containerName+"/")
	return strings.TrimSuffix(p, "/master.m3u8")
}
