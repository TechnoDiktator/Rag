package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

const (
	tokenIssuer   = "atlasrag"
	tokenAudience = "atlasrag-api"
	tokenLifetime = 15 * time.Minute
	hashPrefix    = "$argon2id$v=19$m=65536,t=3,p=4$"
)

type contextKey struct{}

func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(contextKey{}).(uuid.UUID)
	return id, ok
}

func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

type Service struct {
	db     *pgxpool.Pool
	key    ed25519.PrivateKey
	logger *log.Logger
}

func NewService(db *pgxpool.Pool, privateKeyPath string, logger *log.Logger) (*Service, error) {
	data, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read JWT private key: %w", err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("JWT private key must be a PKCS#8 PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse JWT private key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("JWT private key must be Ed25519")
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Service{db: db, key: key, logger: logger}, nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return hashPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}

func verifyPassword(password, encoded string) bool {
	if !strings.HasPrefix(encoded, hashPrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(encoded, hashPrefix), "$")
	if len(parts) != 2 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[0])
	if err != nil || len(salt) != 16 {
		return false
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(hash) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(actual, hash) == 1
}

func normalizeEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	return email, err == nil && address.Address == email && len(email) <= 254
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var input credentials
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		writeError(w, 400, "invalid request")
		return input, false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid request")
		return input, false
	}
	var ok bool
	input.Email, ok = normalizeEmail(input.Email)
	if !ok || len(input.Password) < 12 || len(input.Password) > 128 {
		writeError(w, 400, "valid email and password of 12 to 128 bytes are required")
		return input, false
	}
	return input, true
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Service) Register(w http.ResponseWriter, r *http.Request) {
	input, ok := readCredentials(w, r)
	if !ok {
		return
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		s.logger.Printf("password hashing: %v", err)
		writeError(w, 500, "registration failed")
		return
	}
	_, err = s.db.Exec(r.Context(), "INSERT INTO users (email, password_hash) VALUES ($1, $2)", input.Email, hash)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, 409, "account already exists")
			return
		}
		s.logger.Printf("register user: %v", err)
		writeError(w, 500, "registration failed")
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Service) token(id uuid.UUID, now time.Time) (string, error) {
	claims := jwt.RegisteredClaims{
		Subject: id.String(), Issuer: tokenIssuer, Audience: jwt.ClaimStrings{tokenAudience},
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(tokenLifetime)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(s.key)
}

func (s *Service) Login(w http.ResponseWriter, r *http.Request) {
	input, ok := readCredentials(w, r)
	if !ok {
		return
	}
	var id uuid.UUID
	var hash string
	err := s.db.QueryRow(r.Context(), "SELECT id, password_hash FROM users WHERE email = $1", input.Email).Scan(&id, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Printf("login lookup: %v", err)
		writeError(w, 500, "login failed")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Perform the same expensive password derivation for unknown accounts.
		hash = hashPrefix + "AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	if !verifyPassword(input.Password, hash) || errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 401, "invalid email or password")
		return
	}
	token, err := s.token(id, time.Now())
	if err != nil {
		s.logger.Printf("sign token: %v", err)
		writeError(w, 500, "login failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": int(tokenLifetime.Seconds())}); err != nil {
		s.logger.Printf("write login response: %v", err)
	}
}

func (s *Service) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.Fields(r.Header.Get("Authorization"))
		if len(header) != 2 || !strings.EqualFold(header[0], "Bearer") {
			writeError(w, 401, "authentication required")
			return
		}
		claims := &jwt.RegisteredClaims{}
		token, err := jwt.ParseWithClaims(header[1], claims, func(token *jwt.Token) (any, error) {
			return s.key.Public(), nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}), jwt.WithIssuer(tokenIssuer),
			jwt.WithAudience(tokenAudience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
		if err != nil || !token.Valid {
			writeError(w, 401, "invalid or expired access token")
			return
		}
		id, err := uuid.Parse(claims.Subject)
		if err != nil || id == uuid.Nil {
			writeError(w, 401, "invalid access token")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), id)))
	})
}
