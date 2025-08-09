package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/streamhive/playback-service/internal/db"
	"github.com/streamhive/playback-service/internal/playback"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("logger: %v", err)
	}
	defer logger.Sync()
	logr := logger.Sugar()

	database, err := db.NewConnection()
	if err != nil {
		logr.Fatalf("db: %v", err)
	}

	h := playback.NewHandler(database, logr)

	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/health", h.Ready)
	r.GET("/playback/videos/:uploadId", h.GetDescriptor)
	r.GET("/playback/videos/:uploadId/master.m3u8", h.GetMaster)
	r.GET("/playback/videos/:uploadId/:rendition/index.m3u8", h.GetVariant)
	r.GET("/playback/videos/:uploadId/:rendition/:segment", h.GetSegment)

	port := getEnv("PORT", "8090")
	srv := &http.Server{Addr: ":" + port, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	logr.Infow("playback service listening", "port", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logr.Fatalf("listen: %v", err)
	}
}

func getEnv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
