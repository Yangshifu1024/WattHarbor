package control

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/ChargePilot2026/charge-pilot/internal/gateway/store"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/serviceclient"
	"github.com/gin-gonic/gin"
)

var scanCodePattern = regexp.MustCompile(`^[A-Za-z0-9_:-]{1,64}$`)

type ScanAPI struct {
	Store interface {
		ResolveScan(context.Context, string) (store.ScanResult, error)
	}
	ServiceToken string
}

func (a ScanAPI) Register(router *gin.Engine) {
	router.GET("/api/v1/internal/scan/resolve", a.resolve)
}

func (a ScanAPI) resolve(c *gin.Context) {
	if !serviceclient.VerifyServiceToken(a.ServiceToken, c.GetHeader("X-Service-Token")) {
		httpapi.Write(c, http.StatusUnauthorized, httpapi.CodeUnauthorized, "service token invalid", nil)
		return
	}
	code := c.Query("code")
	if !scanCodePattern.MatchString(code) {
		httpapi.BadRequest(c, "invalid scan code")
		return
	}
	result, err := a.Store.ResolveScan(c.Request.Context(), code)
	if errors.Is(err, store.ErrScanNotFound) {
		httpapi.Write(c, http.StatusNotFound, 1004, "scan code not found", nil)
		return
	}
	if err != nil {
		httpapi.Write(c, http.StatusServiceUnavailable, 5001, "device lookup unavailable", nil)
		return
	}
	httpapi.OK(c, result)
}
