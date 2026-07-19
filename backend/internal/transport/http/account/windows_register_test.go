package account_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	accounthttp "github.com/chenyme/grok2api/backend/internal/transport/http/account"
	"github.com/gin-gonic/gin"
)

func TestWindowsRegisterStatusWithoutService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accounthttp.NewHandler(nil, nil).Register(router.Group("/api/admin/v1"))

	req := httptest.NewRequest(http.MethodGet, "/api/admin/v1/accounts/windows-register/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data struct {
			PlatformSupported bool     `json:"platformSupported"`
			Ready             bool     `json:"ready"`
			Missing           []string `json:"missing"`
			State             string   `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.PlatformSupported || payload.Data.Ready || payload.Data.State != "idle" {
		t.Fatalf("payload=%+v", payload.Data)
	}
}

func TestWindowsRegisterStartWithoutService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accounthttp.NewHandler(nil, nil).Register(router.Group("/api/admin/v1"))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/v1/accounts/windows-register/start", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
