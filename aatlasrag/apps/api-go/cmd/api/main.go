package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"atlasrag/api-go/internal/documents"
	"atlasrag/api-go/internal/objectstore"
	"atlasrag/api-go/internal/upload"
)

const defaultMaxUploadBytes int64 = 50 << 20

func main() {
	logger := log.New(os.Stderr, "atlasrag-api ", log.LstdFlags|log.LUTC)

	databaseURL, err := requiredEnv("DATABASE_URL")
	if err != nil {
		logger.Fatal(err)
	}
	s3Endpoint, err := requiredEnv("S3_ENDPOINT")
	if err != nil {
		logger.Fatal(err)
	}
	s3AccessKey, err := requiredEnv("S3_ACCESS_KEY")
	if err != nil {
		logger.Fatal(err)
	}
	s3SecretKey, err := requiredEnv("S3_SECRET_KEY")
	if err != nil {
		logger.Fatal(err)
	}
	s3Bucket, err := requiredEnv("S3_BUCKET")
	if err != nil {
		logger.Fatal(err)
	}
	maxUploadBytes, err := maxUploadSize()
	if err != nil {
		logger.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.Fatalf("configure PostgreSQL: %v", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		logger.Fatalf("connect to PostgreSQL: %v", err)
	}

	useSSL, err := optionalBoolEnv("S3_USE_SSL", false)
	if err != nil {
		logger.Fatal(err)
	}
	minioClient, err := minio.New(s3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(s3AccessKey, s3SecretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		logger.Fatalf("configure object storage: %v", err)
	}
	bucketExists, err := minioClient.BucketExists(ctx, s3Bucket)
	if err != nil {
		logger.Fatalf("check object-storage bucket: %v", err)
	}
	if !bucketExists {
		logger.Fatalf("object-storage bucket %q does not exist", s3Bucket)
	}

	store := objectstore.NewMinIO(minioClient, s3Bucket)
	repository := documents.NewRepository(db)
	uploadHandler := upload.NewHandler(repository, store, maxUploadBytes, logger)

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("POST /api/v1/documents", uploadHandler)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Printf("API listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("serve API: %v", err)
	}
}

func requiredEnv(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", errors.New(name + " is required")
	}
	return value, nil
}

func optionalBoolEnv(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New(name + " must be true or false")
	}
	return parsed, nil
}

func maxUploadSize() (int64, error) {
	value := os.Getenv("MAX_UPLOAD_BYTES")
	if value == "" {
		return defaultMaxUploadBytes, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > (1<<63-1)-(1<<20)-1 {
		return 0, errors.New("MAX_UPLOAD_BYTES must be a positive integer within the supported range")
	}
	return parsed, nil
}
