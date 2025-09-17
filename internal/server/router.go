package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/f-sync/fsync/internal/matrix"
)

const (
	comparisonRoutePath               = "/"
	healthRoutePath                   = "/healthz"
	htmlContentType                   = "text/html; charset=utf-8"
	errorMessageComparisonUnavailable = "comparison data unavailable"
	errorMessageRenderFailure         = "comparison page rendering failed"
	healthStatusKey                   = "status"
	healthStatusOK                    = "ok"
	logMessageRenderFailure           = "comparison render failure"
	logMessageMissingComparisonData   = "comparison data not loaded"
	ginModeRelease                    = "release"
)

// ComparisonData contains the account sets and owner metadata used to build the comparison.
type ComparisonData struct {
	AccountSetsA matrix.AccountSets
	AccountSetsB matrix.AccountSets
	OwnerA       matrix.OwnerIdentity
	OwnerB       matrix.OwnerIdentity
}

// ComparisonService encapsulates the logic required to build and render comparison pages.
type ComparisonService interface {
	BuildComparison(accountSetsA matrix.AccountSets, accountSetsB matrix.AccountSets, ownerA matrix.OwnerIdentity, ownerB matrix.OwnerIdentity) matrix.ComparisonResult
	RenderComparisonPage(comparison matrix.ComparisonResult) (string, error)
}

// MatrixComparisonService implements ComparisonService by delegating to the matrix package.
type MatrixComparisonService struct{}

// BuildComparison uses matrix.BuildComparison to construct the result.
func (MatrixComparisonService) BuildComparison(accountSetsA matrix.AccountSets, accountSetsB matrix.AccountSets, ownerA matrix.OwnerIdentity, ownerB matrix.OwnerIdentity) matrix.ComparisonResult {
	return matrix.BuildComparison(accountSetsA, accountSetsB, ownerA, ownerB)
}

// RenderComparisonPage uses matrix.RenderComparisonPage to produce the HTML output.
func (MatrixComparisonService) RenderComparisonPage(comparison matrix.ComparisonResult) (string, error) {
	return matrix.RenderComparisonPage(comparison)
}

// RouterConfig configures the HTTP routing for comparison requests.
type RouterConfig struct {
	ComparisonData *ComparisonData
	Service        ComparisonService
	Logger         *zap.Logger
}

// NewRouter constructs a Gin engine configured with the comparison and health handlers.
func NewRouter(configuration RouterConfig) (*gin.Engine, error) {
	service := configuration.Service
	if service == nil {
		service = MatrixComparisonService{}
	}
	logger := configuration.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	gin.SetMode(ginModeRelease)
	engine := gin.New()
	engine.Use(gin.Recovery())

	handler := comparisonHandler{
		data:    configuration.ComparisonData,
		service: service,
		logger:  logger,
	}

	engine.GET(comparisonRoutePath, handler.serveComparison)
	engine.GET(healthRoutePath, handler.healthStatus)

	return engine, nil
}

type comparisonHandler struct {
	data    *ComparisonData
	service ComparisonService
	logger  *zap.Logger
}

func (handler comparisonHandler) serveComparison(ginContext *gin.Context) {
	if handler.data == nil {
		handler.logger.Error(logMessageMissingComparisonData)
		ginContext.String(http.StatusInternalServerError, errorMessageComparisonUnavailable)
		return
	}

	comparison := handler.service.BuildComparison(
		handler.data.AccountSetsA,
		handler.data.AccountSetsB,
		handler.data.OwnerA,
		handler.data.OwnerB,
	)
	pageHTML, err := handler.service.RenderComparisonPage(comparison)
	if err != nil {
		handler.logger.Error(logMessageRenderFailure, zap.Error(err))
		ginContext.String(http.StatusInternalServerError, errorMessageRenderFailure)
		return
	}
	ginContext.Data(http.StatusOK, htmlContentType, []byte(pageHTML))
}

func (handler comparisonHandler) healthStatus(ginContext *gin.Context) {
	ginContext.JSON(http.StatusOK, map[string]string{healthStatusKey: healthStatusOK})
}
