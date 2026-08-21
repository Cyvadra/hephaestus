package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/auth"
	"github.com/gin-gonic/gin"
)

func TestAuthenticationProtectsAPIAndSwagger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAuthService(t)
	srv := New(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, path := range []string{"/api/v1/sessions", "/swagger/index.html"} {
		recorder := httptest.NewRecorder()
		srv.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, recorder.Code)
		}
	}

	token := loginToken(t, svc)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	srv.engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"username":"admin"`) {
		t.Fatalf("session response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLoginAndLogoutCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAuthService(t)
	srv := New(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	now := time.Now()
	salt := "0123456789abcdef0123456789abcdef"
	body := fmt.Sprintf(`{"username":"admin","timestamp":%d,"salt":"%s","digest":"%s"}`, now.UnixMilli(), salt, testDigest("password", now.UnixMilli(), salt))
	loginRecorder := httptest.NewRecorder()
	srv.engine.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body)))
	if loginRecorder.Code != http.StatusOK || loginRecorder.Header().Get(auth.TokenHeader) == "" || len(loginRecorder.Result().Cookies()) != 1 {
		t.Fatalf("login response = %d %v", loginRecorder.Code, loginRecorder.Header())
	}
	logoutRecorder := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(loginRecorder.Result().Cookies()[0])
	srv.engine.ServeHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusNoContent || len(logoutRecorder.Result().Cookies()) != 1 || logoutRecorder.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout response = %d %v", logoutRecorder.Code, logoutRecorder.Header())
	}
}

func TestLoginRequiresProofOfWorkAfterExcessFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := New(newAuthService(t), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for attempt := 0; attempt < auth.FailureThreshold; attempt++ {
		now := time.Now()
		salt := fmt.Sprintf("%032x", attempt+1)
		body := fmt.Sprintf(`{"username":"admin","timestamp":%d,"salt":"%s","digest":"%s"}`, now.UnixMilli(), salt, testDigest("wrong", now.UnixMilli(), salt))
		recorder := httptest.NewRecorder()
		srv.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body)))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d status = %d", attempt+1, recorder.Code)
		}
	}

	now := time.Now()
	salt := "0123456789abcdef0123456789abcdef"
	body := fmt.Sprintf(`{"username":"admin","timestamp":%d,"salt":"%s","digest":"%s"}`, now.UnixMilli(), salt, testDigest("password", now.UnixMilli(), salt))
	recorder := httptest.NewRecorder()
	srv.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body)))
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), `"error":"proof_of_work_required"`) || !strings.Contains(recorder.Body.String(), `"difficulty":18`) {
		t.Fatalf("challenge response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func newAuthService(t *testing.T) *auth.Service {
	t.Helper()
	svc, err := auth.New(auth.Config{Username: "admin", Password: "password", Secret: "12345678901234567890123456789012"})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func loginToken(t *testing.T, svc *auth.Service) string {
	t.Helper()
	now := time.Now()
	salt := "0123456789abcdef0123456789abcdef"
	token, err := svc.Login("admin", now.UnixMilli(), salt, testDigest("password", now.UnixMilli(), salt))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func testDigest(password string, timestamp int64, salt string) string {
	digest := sha256.Sum256([]byte(password + fmt.Sprintf("%d", timestamp) + salt))
	return hex.EncodeToString(digest[:])
}
