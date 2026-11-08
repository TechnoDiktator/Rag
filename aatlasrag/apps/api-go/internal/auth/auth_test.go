package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func testService(t *testing.T) *Service {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(nil, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPasswordHashVerification(t *testing.T) {
	first, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !verifyPassword("correct horse battery", first) ||
		verifyPassword("incorrect password", first) || verifyPassword("anything", "invalid") {
		t.Fatal("password verification or salt behavior failed")
	}
}

func TestBearerTokenValidation(t *testing.T) {
	s := testService(t)
	user := uuid.New()
	good, err := s.token(user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	expired, err := s.token(user, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	other := testService(t)
	foreign, err := other.token(user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wrongAudience, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{
		Subject: user.String(), Issuer: tokenIssuer, Audience: jwt.ClaimStrings{"other"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).SignedString(s.key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, header string
		want         int
	}{
		{"valid", "Bearer " + good, 204},
		{"missing", "", 401},
		{"expired", "Bearer " + expired, 401},
		{"wrong signature", "Bearer " + foreign, 401},
		{"wrong audience", "Bearer " + wrongAudience, 401},
		{"malformed", "Bearer not-a-token", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := s.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, ok := UserID(r.Context())
				if !ok || id != user {
					t.Errorf("wrong identity: %v %v", id, ok)
				}
				w.WriteHeader(204)
			}))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", tc.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("got %d, want %d", response.Code, tc.want)
			}
		})
	}
}

func TestRejectInvalidPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a private key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(nil, path, nil); err == nil {
		t.Fatal("accepted invalid key")
	}
}
