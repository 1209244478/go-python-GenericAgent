package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// m1: CORS 白名单回显
func TestCORSMiddleware_WhitelistEcho(t *testing.T) {
	allowed := []string{"https://app.example.com", "https://admin.example.com"}
	r := gin.New()
	r.Use(CORSMiddleware(allowed))
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	// 允许的 origin 应被回显
	req := httptest.NewRequest("GET", "/ping", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("CORS echo mismatch: got %q, want https://app.example.com", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("credentials header mismatch: got %q", got)
	}
}

func TestCORSMiddleware_RejectsUnknownOrigin(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://allowed.com"}))
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	req := httptest.NewRequest("GET", "/ping", nil)
	req.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unknown origin should not get ACAO header, got %q", got)
	}
}

func TestCORSMiddleware_WildcardAllowsAll(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"*"}))
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	req := httptest.NewRequest("GET", "/ping", nil)
	req.Header.Set("Origin", "https://anything.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.com" {
		t.Errorf("wildcard should echo origin, got %q", got)
	}
}

func TestCORSMiddleware_PreflightShortCircuit(t *testing.T) {
	r := gin.New()
	r.Use(CORSMiddleware([]string{"https://app.com"}))
	r.GET("/ping", func(c *gin.Context) { c.String(200, "pong") })

	req := httptest.NewRequest("OPTIONS", "/ping", nil)
	req.Header.Set("Origin", "https://app.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight should return 204, got %d", w.Code)
	}
}

// m1: BodyLimitMiddleware
func TestBodyLimitMiddleware_RejectsOversize(t *testing.T) {
	r := gin.New()
	r.Use(BodyLimitMiddleware(16))
	r.POST("/data", func(c *gin.Context) { c.String(200, "ok") })

	body := strings.Repeat("x", 100)
	req := httptest.NewRequest("POST", "/data", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", w.Code)
	}
}

func TestBodyLimitMiddleware_AllowsWithinLimit(t *testing.T) {
	r := gin.New()
	r.Use(BodyLimitMiddleware(1024))
	r.POST("/data", func(c *gin.Context) { c.String(200, "ok") })

	req := httptest.NewRequest("POST", "/data", strings.NewReader("small body"))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}
