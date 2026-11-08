package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/segmentio/kafka-go"

	"atlasrag/api-go/internal/auth"
	"atlasrag/api-go/internal/documents"
	"atlasrag/api-go/internal/objectstore"
	"atlasrag/api-go/internal/outbox"
	"atlasrag/api-go/internal/search"
	"atlasrag/api-go/internal/upload"
	"atlasrag/api-go/internal/web"
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
	kafkaBrokers := kafkaBrokerList()
	jwtKeyPath, err := requiredEnv("JWT_PRIVATE_KEY_PATH")
	if err != nil {
		logger.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		logger.Fatalf("configure PostgreSQL: %v", err)
	}
	if err := db.Ping(ctx); err != nil {
		cancel()
		db.Close()
		logger.Fatalf("connect to PostgreSQL: %v", err)
	}
	var schemaReady bool
	err = db.QueryRow(ctx, `
		SELECT to_regclass('public.users') IS NOT NULL
			AND to_regclass('public.projects') IS NOT NULL
			AND to_regclass('public.project_members') IS NOT NULL
			AND EXISTS (SELECT 1 FROM information_schema.columns
				WHERE table_schema='public' AND table_name='documents' AND column_name='project_id')
	`).Scan(&schemaReady)
	if err != nil || !schemaReady {
		cancel()
		db.Close()
		logger.Fatalf("project isolation schema unavailable; apply migration 000006: %v", err)
	}
	cancel()
	authService, err := auth.NewService(db, jwtKeyPath, logger)
	if err != nil {
		db.Close()
		logger.Fatalf("configure authentication: %v", err)
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
		db.Close()
		logger.Fatalf("configure object storage: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	bucketExists, err := minioClient.BucketExists(ctx, s3Bucket)
	cancel()
	if err != nil {
		db.Close()
		logger.Fatalf("check object-storage bucket: %v", err)
	}
	if !bucketExists {
		db.Close()
		logger.Fatalf("object-storage bucket %q does not exist", s3Bucket)
	}

	store := objectstore.NewMinIO(minioClient, s3Bucket)
	repository := documents.NewRepository(db)
	uploadHandler := upload.NewHandler(repository, store, maxUploadBytes, logger)
	statusHandler := documents.NewStatusHandler(repository, logger)
	publisher := outbox.NewPublisher(db, &kafka.Writer{
		Addr:         kafka.TCP(kafkaBrokers...),
		Topic:        "document.uploaded",
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
	}, logger)

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	publisherDone := make(chan struct{})
	go func() {
		defer close(publisherDone)
		publisher.Run(runCtx)
	}()

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/v1/auth/register", authService.Register)
	mux.HandleFunc("POST /api/v1/auth/login", authService.Login)
	mux.Handle("POST /api/v1/projects", authService.Authenticate(http.HandlerFunc(authService.CreateProject)))
	mux.Handle("GET /api/v1/projects", authService.Authenticate(http.HandlerFunc(authService.ListProjects)))
	mux.Handle("POST /api/v1/projects/{projectID}/members", authService.ProjectAccess(http.HandlerFunc(authService.AddMember), false))
	mux.Handle("GET /api/v1/projects/{projectID}/documents", authService.ProjectAccess(http.HandlerFunc(authService.ListDocuments), false))
	mux.Handle("POST /api/v1/projects/{projectID}/documents", authService.ProjectAccess(uploadHandler, false))
	mux.Handle("GET /api/v1/projects/{projectID}/documents/{documentID}/status", authService.ProjectAccess(statusHandler, true))
	mux.Handle("GET /api/v1/projects/{projectID}/documents/{documentID}/chunks", authService.ProjectAccess(documents.NewChunkHandler(repository, logger), true))
	mux.Handle("GET /api/v1/projects/{projectID}/documents/{documentID}/pdf", authService.ProjectAccess(documents.NewPDFHandler(repository, store, logger), true))
	var searchEmbedder search.Embedder
	var answerGenerator search.AnswerGenerator
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		searchEmbedder = search.NewOpenAIEmbedder(key)
		answerGenerator = search.NewOpenAIAnswerGenerator(key)
	}
	searchHandler := search.NewHandler(search.NewRepository(db), searchEmbedder, logger)
	mux.Handle("POST /api/v1/projects/{projectID}/search", authService.ProjectAccess(searchHandler, false))
	mux.Handle("POST /api/v1/projects/{projectID}/answer", authService.ProjectAccess(search.NewAnswerHandler(searchHandler, answerGenerator), false))
	frontend := web.Handler()
	mux.Handle("GET /{$}", frontend)
	mux.Handle("GET /app.css", frontend)
	mux.Handle("GET /app.js", frontend)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Printf("API listening on %s", addr)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("serve API: %v", err)
		}
	case <-runCtx.Done():
	}

	stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shut down API: %v", err)
	}
	<-publisherDone
	if err := publisher.Close(); err != nil {
		logger.Printf("close Kafka publisher: %v", err)
	}
	db.Close()
}

func kafkaBrokerList() []string {
	value := os.Getenv("KAFKA_BROKERS")
	if value == "" {
		value = "localhost:9092"
	}
	var brokers []string
	for _, broker := range strings.Split(value, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			brokers = append(brokers, broker)
		}
	}
	if len(brokers) == 0 {
		log.Fatal("KAFKA_BROKERS must contain at least one broker address")
	}
	return brokers
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
