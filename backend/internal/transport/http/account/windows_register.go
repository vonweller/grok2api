package account

import (
	"errors"
	"io"
	"net/http"
	"strings"

	windowsregisterapp "github.com/chenyme/grok2api/backend/internal/application/windowsregister"
	windowsregisterinfra "github.com/chenyme/grok2api/backend/internal/infra/windowsregister"
	"github.com/chenyme/grok2api/backend/internal/shared/response"
	"github.com/gin-gonic/gin"
)

type windowsRegisterStartRequest struct {
	Target      int    `json:"target"`
	EmailMode   string `json:"emailMode"`
	EmailAPI    string `json:"emailApi"`
	EmailDomain string `json:"emailDomain"`
	Proxy       string `json:"proxy"`
	MaxMem      string `json:"maxMem"`
	Debug       bool   `json:"debug"`
}

type windowsRegisterImportRequest struct {
	Scope        string   `json:"scope"`
	Destinations []string `json:"destinations"`
}

func (h *Handler) windowsRegisterStatus(c *gin.Context) {
	if h.windowsRegister == nil {
		response.Success(c, http.StatusOK, unavailableWindowsRegisterStatus())
		return
	}
	response.Success(c, http.StatusOK, h.windowsRegister.Status())
}

func (h *Handler) windowsRegisterStart(c *gin.Context) {
	if h.windowsRegister == nil {
		response.Error(c, http.StatusServiceUnavailable, "windowsRegisterUnavailable", "Windows 注册机在当前平台或部署中不可用")
		return
	}
	var request windowsRegisterStartRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "启动参数无效")
		return
	}
	status, err := h.windowsRegister.Start(windowsregisterinfra.StartOptions{
		Target:      request.Target,
		EmailMode:   request.EmailMode,
		EmailAPI:    request.EmailAPI,
		EmailDomain: request.EmailDomain,
		Proxy:       request.Proxy,
		MaxMem:      request.MaxMem,
		Debug:       request.Debug,
	})
	if err != nil {
		writeWindowsRegisterError(c, err)
		return
	}
	response.Success(c, http.StatusOK, status)
}

func (h *Handler) windowsRegisterStop(c *gin.Context) {
	if h.windowsRegister == nil {
		response.Error(c, http.StatusServiceUnavailable, "windowsRegisterUnavailable", "Windows 注册机在当前平台或部署中不可用")
		return
	}
	status, err := h.windowsRegister.Stop(c.Request.Context())
	if err != nil {
		writeWindowsRegisterError(c, err)
		return
	}
	response.Success(c, http.StatusOK, status)
}

func (h *Handler) windowsRegisterImport(c *gin.Context) {
	if h.windowsRegister == nil {
		response.Error(c, http.StatusServiceUnavailable, "windowsRegisterUnavailable", "Windows 注册机在当前平台或部署中不可用")
		return
	}
	var request windowsRegisterImportRequest
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		response.Error(c, http.StatusBadRequest, "invalidRequest", "导入参数无效")
		return
	}
	result, err := h.windowsRegister.Import(c.Request.Context(), windowsregisterapp.ImportRequest{
		Scope:        request.Scope,
		Destinations: request.Destinations,
	})
	if err != nil {
		writeWindowsRegisterError(c, err)
		return
	}
	response.Success(c, http.StatusOK, result)
}

func writeWindowsRegisterError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, windowsregisterinfra.ErrPlatformUnsupported):
		response.Error(c, http.StatusServiceUnavailable, "windowsRegisterUnavailable", "Windows 注册机仅在 Windows 上可用")
	case errors.Is(err, windowsregisterinfra.ErrNotReady):
		response.Error(c, http.StatusServiceUnavailable, "windowsRegisterNotReady", err.Error())
	case errors.Is(err, windowsregisterinfra.ErrAlreadyRunning):
		response.Error(c, http.StatusConflict, "windowsRegisterRunning", "Windows 注册机已在运行")
	case errors.Is(err, windowsregisterinfra.ErrNoImportableAccounts):
		response.Error(c, http.StatusBadRequest, "windowsRegisterEmpty", "没有可导入的注册结果")
	case errors.Is(err, windowsregisterinfra.ErrInvalidStartOptions):
		response.Error(c, http.StatusBadRequest, "invalidRequest", err.Error())
	default:
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = "Windows 注册机操作失败"
		}
		response.Error(c, http.StatusInternalServerError, "windowsRegisterFailed", message)
	}
}

func unavailableWindowsRegisterStatus() windowsregisterinfra.Status {
	return windowsregisterinfra.Status{
		PlatformSupported: false,
		Ready:             false,
		Missing:           []string{"service"},
		State:             windowsregisterinfra.StateIdle,
		Logs:              []string{},
	}
}
