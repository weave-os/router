package admin_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/escalationdashboard"
)

type escalationDashboardHandlerStore struct {
	filter escalationdashboard.Filter
	err    error
}

func (s *escalationDashboardHandlerStore) CreateSnapshot(_ context.Context, filter escalationdashboard.Filter) (escalationdashboard.StoredSnapshot, error) {
	s.filter = filter
	return escalationdashboard.StoredSnapshot{SnapshotID: uuid.NewString()}, s.err
}

func (s *escalationDashboardHandlerStore) SnapshotPage(_ context.Context, _ string, _, _ int32, _ time.Time) (escalationdashboard.StoredSnapshot, error) {
	return escalationdashboard.StoredSnapshot{}, s.err
}

func TestInternalEscalationDashboardHandlerParsesTypedFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &escalationDashboardHandlerStore{}
	service := (&proxy.Service{}).WithEscalationDashboard(store)
	engine := gin.New()
	engine.GET("/internal/v1/escalation/dashboard", admin.InternalEscalationDashboardHandler(service))
	installationID := uuid.NewString()
	request := httptest.NewRequest(http.MethodGet,
		"/internal/v1/escalation/dashboard?service=xgb&mode=shadow&organization_id=org-1&installation_id="+strings.ToUpper(installationID)+"&outcome=recommended&limit=25", nil)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, escalationdashboard.ServiceXGB, store.filter.Service)
	require.Equal(t, escalationdashboard.ModeShadow, store.filter.Mode)
	require.Equal(t, "org-1", store.filter.OrganizationID)
	require.Equal(t, installationID, store.filter.InstallationID)
	require.Equal(t, escalationdashboard.SessionOutcomeRecommended, store.filter.SessionOutcome)
	require.Equal(t, int32(25), store.filter.Limit)
}

func TestInternalEscalationDashboardHandlerRejectsInvalidArguments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := (&proxy.Service{}).WithEscalationDashboard(&escalationDashboardHandlerStore{})
	engine := gin.New()
	engine.GET("/internal/v1/escalation/dashboard", admin.InternalEscalationDashboardHandler(service))
	tests := []string{
		"service=other",
		"mode=off",
		"outcome=negative",
		"installation_id=not-a-uuid",
		"limit=0",
		"limit=201",
		"limit=invalid",
		"cursor=invalid",
	}
	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/internal/v1/escalation/dashboard?"+query, nil)
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestInternalEscalationDashboardHandlerReportsExpiredSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	snapshotID := uuid.NewString()
	cursor, err := escalationdashboard.EncodeCursor(snapshotID, 50)
	require.NoError(t, err)
	service := (&proxy.Service{}).WithEscalationDashboard(&escalationDashboardHandlerStore{err: escalationdashboard.ErrExpiredCursor})
	engine := gin.New()
	engine.GET("/internal/v1/escalation/dashboard", admin.InternalEscalationDashboardHandler(service))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/escalation/dashboard?cursor="+cursor, nil)

	engine.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusGone, recorder.Code)
}
