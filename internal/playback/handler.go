package playback

import (
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/streamhive/playback-service/internal/models"
)

type Handler struct {
	db     *gorm.DB
	log    *zap.SugaredLogger
	client *http.Client
}

func NewHandler(db *gorm.DB, log *zap.SugaredLogger) *Handler {
	return &Handler{db: db, log: log, client: &http.Client{}}
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
	base := baseHLSPath(v.HLSMasterURL)
	url := base + "/" + rendition + "/" + segment
	proxyBinary(c, h.client, url)
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
