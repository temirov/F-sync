package server

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/f-sync/fsync/internal/matrix"
)

const (
	comparisonRoutePath             = "/"
	healthRoutePath                 = "/healthz"
	uploadsRoutePath                = "/api/uploads"
	staticRoutePath                 = "/static"
	htmlContentType                 = "text/html; charset=utf-8"
	jsonContentType                 = "application/json; charset=utf-8"
	healthStatusKey                 = "status"
	healthStatusOK                  = "ok"
	uploadFormFieldName             = "archives"
	tempFilePattern                 = "fsync-upload-*.zip"
	slotLabelPrimary                = "Archive A"
	slotLabelSecondary              = "Archive B"
	ownerHandlePrefix               = "@"
	unknownOwnerLabel               = "Unknown"
	errMessageNoFilesUploaded       = "no files were uploaded"
	errMessageInvalidArchive        = "uploaded file must be a Twitter archive zip"
	errMessageStoreUpdate           = "unable to store uploaded archive"
	errMessageTooManyArchives       = "two archives already uploaded; reset before adding more"
	errMessageRenderFailure         = "comparison page rendering failed"
	errMessageUploadPersistFailure  = "unable to persist uploaded file"
	logMessageRenderFailure         = "comparison render failure"
	logMessageStoreFailure          = "upload store failure"
	logMessageArchiveParseFailure   = "archive parse failure"
	logMessageHandleResolution      = "resolving handles"
	logMessageHandleResolutionError = "handle resolution failure"
	logFieldArchiveName             = "archive"
	logFieldAccountID               = "account_id"
	ginModeRelease                  = "release"
)

var (
	errNoFilesUploaded = errors.New(errMessageNoFilesUploaded)
	errTooManyArchives = errors.New(errMessageTooManyArchives)
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
	RenderComparisonPage(pageData matrix.ComparisonPageData) (string, error)
}

// MatrixComparisonService implements ComparisonService by delegating to the matrix package.
type MatrixComparisonService struct{}

// BuildComparison uses matrix.BuildComparison to construct the result.
func (MatrixComparisonService) BuildComparison(accountSetsA matrix.AccountSets, accountSetsB matrix.AccountSets, ownerA matrix.OwnerIdentity, ownerB matrix.OwnerIdentity) matrix.ComparisonResult {
	return matrix.BuildComparison(accountSetsA, accountSetsB, ownerA, ownerB)
}

// RenderComparisonPage uses matrix.RenderComparisonPage to produce the HTML output.
func (MatrixComparisonService) RenderComparisonPage(pageData matrix.ComparisonPageData) (string, error) {
	return matrix.RenderComparisonPage(pageData)
}

// RouterConfig configures the HTTP routing for comparison requests.
type RouterConfig struct {
	Service        ComparisonService
	Store          ComparisonStore
	Logger         *zap.Logger
	HandleResolver matrix.AccountHandleResolver
}

// ComparisonStore persists uploaded archives and exposes comparison snapshots.
type ComparisonStore interface {
	Snapshot() ComparisonSnapshot
	Upsert(upload ArchiveUpload) (ComparisonSnapshot, error)
	Clear() ComparisonSnapshot
	ResolveHandles(ctx context.Context, resolver matrix.AccountHandleResolver) map[string]error
}

// ComparisonSnapshot represents the current upload state and optional comparison data.
type ComparisonSnapshot struct {
	Uploads        []matrix.UploadSummary
	ComparisonData *ComparisonData
}

// ArchiveUpload captures the data parsed from an uploaded archive.
type ArchiveUpload struct {
	FileName    string
	AccountSets matrix.AccountSets
	Owner       matrix.OwnerIdentity
}

// NewRouter constructs a Gin engine configured with comparison, upload, static, and health handlers.
func NewRouter(configuration RouterConfig) (*gin.Engine, error) {
	service := configuration.Service
	if service == nil {
		service = MatrixComparisonService{}
	}
	logger := configuration.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	store := configuration.Store
	if store == nil {
		store = newMemoryComparisonStore()
	}

	gin.SetMode(ginModeRelease)
	engine := gin.New()
	engine.Use(gin.Recovery())

	staticFiles, err := matrix.StaticAssets()
	if err != nil {
		return nil, fmt.Errorf("load static assets: %w", err)
	}
	engine.StaticFS(staticRoutePath, http.FS(staticFiles))

	handler := applicationHandler{
		store:          store,
		service:        service,
		logger:         logger,
		handleResolver: configuration.HandleResolver,
	}

	engine.GET(comparisonRoutePath, handler.serveComparison)
	engine.GET(healthRoutePath, handler.healthStatus)
	engine.POST(uploadsRoutePath, handler.uploadArchives)
	engine.DELETE(uploadsRoutePath, handler.resetArchives)

	return engine, nil
}

type applicationHandler struct {
	store          ComparisonStore
	service        ComparisonService
	logger         *zap.Logger
	handleResolver matrix.AccountHandleResolver
}

func (handler applicationHandler) serveComparison(ginContext *gin.Context) {
	snapshot := handler.store.Snapshot()
	var comparisonResult *matrix.ComparisonResult
	if snapshot.ComparisonData != nil {
		result := handler.service.BuildComparison(
			snapshot.ComparisonData.AccountSetsA,
			snapshot.ComparisonData.AccountSetsB,
			snapshot.ComparisonData.OwnerA,
			snapshot.ComparisonData.OwnerB,
		)
		comparisonResult = &result
	}

	pageHTML, err := handler.service.RenderComparisonPage(matrix.ComparisonPageData{
		Comparison: comparisonResult,
		Uploads:    snapshot.Uploads,
	})
	if err != nil {
		handler.logger.Error(logMessageRenderFailure, zap.Error(err))
		ginContext.Data(http.StatusInternalServerError, htmlContentType, []byte(errMessageRenderFailure))
		return
	}
	ginContext.Data(http.StatusOK, htmlContentType, []byte(pageHTML))
}

func (handler applicationHandler) healthStatus(ginContext *gin.Context) {
	ginContext.JSON(http.StatusOK, map[string]string{healthStatusKey: healthStatusOK})
}

func (handler applicationHandler) uploadArchives(ginContext *gin.Context) {
	multipartForm, err := ginContext.MultipartForm()
	if err != nil {
		handler.writeJSONError(ginContext, http.StatusBadRequest, errMessageNoFilesUploaded)
		return
	}
	files := multipartForm.File[uploadFormFieldName]
	if len(files) == 0 {
		handler.writeJSONError(ginContext, http.StatusBadRequest, errMessageNoFilesUploaded)
		return
	}

	var snapshot ComparisonSnapshot
	for _, fileHeader := range files {
		tempPath, cleanup, saveErr := handler.saveUploadedFile(ginContext, fileHeader)
		if saveErr != nil {
			handler.logger.Error(logMessageArchiveParseFailure, zap.Error(saveErr), zap.String(logFieldArchiveName, fileHeader.Filename))
			handler.writeJSONError(ginContext, http.StatusInternalServerError, errMessageUploadPersistFailure)
			if cleanup != nil {
				cleanup()
			}
			return
		}

		accountSets, owner, parseErr := matrix.ReadTwitterZip(tempPath)
		if cleanup != nil {
			cleanup()
		}
		if parseErr != nil {
			handler.logger.Error(logMessageArchiveParseFailure, zap.Error(parseErr), zap.String(logFieldArchiveName, fileHeader.Filename))
			handler.writeJSONError(ginContext, http.StatusBadRequest, fmt.Sprintf("%s: %v", errMessageInvalidArchive, parseErr))
			return
		}

		upload := ArchiveUpload{FileName: fileHeader.Filename, AccountSets: accountSets, Owner: owner}
		snapshot, err = handler.store.Upsert(upload)
		if err != nil {
			handler.logger.Error(logMessageStoreFailure, zap.Error(err), zap.String(logFieldArchiveName, fileHeader.Filename))
			if errors.Is(err, errTooManyArchives) {
				handler.writeJSONError(ginContext, http.StatusBadRequest, err.Error())
			} else {
				handler.writeJSONError(ginContext, http.StatusInternalServerError, errMessageStoreUpdate)
			}
			return
		}
	}

	if handler.handleResolver != nil && snapshot.ComparisonData != nil {
		handler.logger.Info(logMessageHandleResolution)
		go handler.resolveHandlesAsync()
	}

	ginContext.Header("Content-Type", jsonContentType)
	ginContext.JSON(http.StatusOK, uploadResponse{
		Uploads:         snapshot.Uploads,
		ComparisonReady: snapshot.ComparisonData != nil,
	})
}

func (handler applicationHandler) resetArchives(ginContext *gin.Context) {
	handler.store.Clear()
	ginContext.Status(http.StatusNoContent)
}

func (handler applicationHandler) resolveHandlesAsync() {
	errorsByAccountID := handler.store.ResolveHandles(context.Background(), handler.handleResolver)
	for accountID, resolutionErr := range errorsByAccountID {
		handler.logger.Warn(logMessageHandleResolutionError, zap.String(logFieldAccountID, accountID), zap.Error(resolutionErr))
	}
}

func (handler applicationHandler) saveUploadedFile(ginContext *gin.Context, fileHeader *multipart.FileHeader) (string, func(), error) {
	tempFile, err := os.CreateTemp("", tempFilePattern)
	if err != nil {
		return "", nil, fmt.Errorf("create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	if closeErr := tempFile.Close(); closeErr != nil {
		_ = os.Remove(tempPath)
		return "", nil, fmt.Errorf("close temp file: %w", closeErr)
	}
	if err := ginContext.SaveUploadedFile(fileHeader, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return "", nil, fmt.Errorf("save upload: %w", err)
	}
	cleanup := func() { _ = os.Remove(tempPath) }
	return tempPath, cleanup, nil
}

func (handler applicationHandler) writeJSONError(ginContext *gin.Context, statusCode int, message string) {
	ginContext.Header("Content-Type", jsonContentType)
	ginContext.JSON(statusCode, errorResponse{Error: message})
}

type uploadResponse struct {
	Uploads         []matrix.UploadSummary `json:"uploads"`
	ComparisonReady bool                   `json:"comparisonReady"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type memoryComparisonStore struct {
	mutex     sync.RWMutex
	primary   *archiveRecord
	secondary *archiveRecord
}

type archiveRecord struct {
	slotLabel   string
	fileName    string
	owner       matrix.OwnerIdentity
	accountSets matrix.AccountSets
}

func newMemoryComparisonStore() *memoryComparisonStore {
	return &memoryComparisonStore{}
}

func (store *memoryComparisonStore) Snapshot() ComparisonSnapshot {
	store.mutex.RLock()
	defer store.mutex.RUnlock()
	return store.snapshotLocked()
}

func (store *memoryComparisonStore) Upsert(upload ArchiveUpload) (ComparisonSnapshot, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()

	record := archiveRecord{
		fileName:    upload.FileName,
		owner:       upload.Owner,
		accountSets: copyAccountSets(upload.AccountSets),
	}

	if store.primary == nil {
		record.slotLabel = slotLabelPrimary
		store.primary = &record
		return store.snapshotLocked(), nil
	}
	if sameOwner(store.primary.owner, record.owner) || sameFileForUnknownOwner(*store.primary, record) {
		record.slotLabel = store.primary.slotLabel
		store.primary = &record
		return store.snapshotLocked(), nil
	}
	if store.secondary == nil {
		record.slotLabel = slotLabelSecondary
		store.secondary = &record
		return store.snapshotLocked(), nil
	}
	if sameOwner(store.secondary.owner, record.owner) || sameFileForUnknownOwner(*store.secondary, record) {
		record.slotLabel = store.secondary.slotLabel
		store.secondary = &record
		return store.snapshotLocked(), nil
	}
	return ComparisonSnapshot{}, errTooManyArchives
}

func (store *memoryComparisonStore) Clear() ComparisonSnapshot {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.primary = nil
	store.secondary = nil
	return store.snapshotLocked()
}

func (store *memoryComparisonStore) ResolveHandles(ctx context.Context, resolver matrix.AccountHandleResolver) map[string]error {
	store.mutex.RLock()
	if store.primary == nil || store.secondary == nil {
		store.mutex.RUnlock()
		return nil
	}
	primary := *store.primary
	secondary := *store.secondary
	store.mutex.RUnlock()

	accountSetsPrimary := copyAccountSets(primary.accountSets)
	accountSetsSecondary := copyAccountSets(secondary.accountSets)
	errorsByAccountID := matrix.MaybeResolveHandles(ctx, resolver, true, &accountSetsPrimary, &accountSetsSecondary)

	store.mutex.Lock()
	if store.primary != nil && (sameOwner(store.primary.owner, primary.owner) || sameFileForUnknownOwner(*store.primary, primary)) {
		store.primary.accountSets = accountSetsPrimary
	}
	if store.secondary != nil && (sameOwner(store.secondary.owner, secondary.owner) || sameFileForUnknownOwner(*store.secondary, secondary)) {
		store.secondary.accountSets = accountSetsSecondary
	}
	store.mutex.Unlock()
	return errorsByAccountID
}

func (store *memoryComparisonStore) snapshotLocked() ComparisonSnapshot {
	uploads := make([]matrix.UploadSummary, 0, 2)
	if store.primary != nil {
		uploads = append(uploads, matrix.UploadSummary{SlotLabel: store.primary.slotLabel, OwnerLabel: ownerSummary(store.primary.owner), FileName: store.primary.fileName})
	}
	if store.secondary != nil {
		uploads = append(uploads, matrix.UploadSummary{SlotLabel: store.secondary.slotLabel, OwnerLabel: ownerSummary(store.secondary.owner), FileName: store.secondary.fileName})
	}
	var comparison *ComparisonData
	if store.primary != nil && store.secondary != nil {
		comparison = &ComparisonData{
			AccountSetsA: copyAccountSets(store.primary.accountSets),
			AccountSetsB: copyAccountSets(store.secondary.accountSets),
			OwnerA:       store.primary.owner,
			OwnerB:       store.secondary.owner,
		}
	}
	return ComparisonSnapshot{Uploads: uploads, ComparisonData: comparison}
}

func sameOwner(first matrix.OwnerIdentity, second matrix.OwnerIdentity) bool {
	if strings.TrimSpace(first.AccountID) != "" && strings.TrimSpace(second.AccountID) != "" {
		return strings.EqualFold(first.AccountID, second.AccountID)
	}
	if strings.TrimSpace(first.UserName) != "" && strings.TrimSpace(second.UserName) != "" {
		return strings.EqualFold(first.UserName, second.UserName)
	}
	return false
}

func sameFileForUnknownOwner(existing archiveRecord, incoming archiveRecord) bool {
	if strings.TrimSpace(existing.owner.AccountID) != "" || strings.TrimSpace(incoming.owner.AccountID) != "" {
		return false
	}
	if strings.TrimSpace(existing.owner.UserName) != "" || strings.TrimSpace(incoming.owner.UserName) != "" {
		return false
	}
	if strings.TrimSpace(existing.fileName) == "" || strings.TrimSpace(incoming.fileName) == "" {
		return false
	}
	return strings.EqualFold(existing.fileName, incoming.fileName)
}

func ownerSummary(owner matrix.OwnerIdentity) string {
	display := strings.TrimSpace(owner.DisplayName)
	handle := strings.TrimSpace(owner.UserName)
	switch {
	case display != "" && handle != "":
		return fmt.Sprintf("%s (%s%s)", display, ownerHandlePrefix, handle)
	case display != "":
		return display
	case handle != "":
		return ownerHandlePrefix + handle
	case strings.TrimSpace(owner.AccountID) != "":
		return owner.AccountID
	default:
		return unknownOwnerLabel
	}
}

func copyAccountSets(source matrix.AccountSets) matrix.AccountSets {
	return matrix.AccountSets{
		Followers: copyAccountRecordMap(source.Followers),
		Following: copyAccountRecordMap(source.Following),
		Muted:     copyBoolMap(source.Muted),
		Blocked:   copyBoolMap(source.Blocked),
	}
}

func copyAccountRecordMap(source map[string]matrix.AccountRecord) map[string]matrix.AccountRecord {
	if len(source) == 0 {
		return map[string]matrix.AccountRecord{}
	}
	cloned := make(map[string]matrix.AccountRecord, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func copyBoolMap(source map[string]bool) map[string]bool {
	if len(source) == 0 {
		return map[string]bool{}
	}
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
