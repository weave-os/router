package middleware_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/server/middleware"
	"weave-os/router/internal/sqlc"
)

type admissionDB struct {
	sqlc.DBTX
	reads int
	args  []any
	row   admissionRow
}

func (db *admissionDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	db.reads++
	db.args = args
	return db.row
}

type admissionRow struct {
	override, missing                                    bool
	balance, keySpent, sessionSpent, taskSpent, orgSpent int64
	err                                                  error
}

func (r admissionRow) Scan(d ...any) error {
	if r.err != nil {
		return r.err
	}
	cap := int64(100)
	*d[0].(*bool) = r.override
	*d[1].(*bool) = !r.missing
	*d[2].(*int64) = r.balance
	*d[3].(*bool) = true
	*d[4].(**int64) = &cap
	*d[5].(*int64) = r.keySpent
	*d[6].(*int64) = r.sessionSpent
	*d[7].(*int64) = r.taskSpent
	*d[8].(**int64) = &cap
	*d[9].(*int64) = r.orgSpent
	return nil
}

func TestBillingAdmissionReadsOncePerRequest(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		db := &admissionDB{row: admissionRow{balance: 1}}
		engine := admissionEngine(db, delegated)
		key := pgtype.UUID{Bytes: [16]byte{15: 2}, Valid: true}
		wantArgs := []any{"org_x", key, pgtype.UUID{}, "", ""}
		if delegated {
			issuer := pgtype.UUID{Bytes: [16]byte{15: 3}, Valid: true}
			wantArgs = []any{"org_x", key, issuer, "session", "task"}
		}
		for range 2 {
			before := db.reads
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest("GET", "/v1/messages", nil))
			require.Equal(t, 204, w.Code, w.Body.String())
			require.Equal(t, 1, db.reads-before)
			require.Equal(t, wantArgs, db.args)
		}
	}
}

func admissionEngine(db *admissionDB, delegated bool) *gin.Engine {
	repo := postgres.NewBillingRepo(db)
	svc := billing.NewService(repo).WithAdmissionReader(repo)
	engine := gin.New()
	funding := credentials.RequestHasCustomerFunding
	engine.GET("/v1/messages", func(c *gin.Context) {
		withInstallation(c, "org_x")
		const key = "00000000-0000-0000-0000-000000000002"
		c.Set("router_api_key", &auth.APIKey{ID: key})
		if delegated {
			const issuer = "00000000-0000-0000-0000-000000000003"
			cap := int64(100)
			c.Request = c.Request.WithContext(auth.WithDelegatedSession(c.Request.Context(), &auth.DelegatedSession{IssuerKeyID: issuer, SessionID: "session", TaskID: "task", SessionCapMicros: &cap, TaskCapMicros: &cap}))
		}
	}, middleware.WithBalanceCheck(svc, 0, funding), middleware.WithAPIKeySpendCap(svc, funding), middleware.WithDelegatedSpendCap(svc, funding), middleware.WithOrgMonthlySpendCap(svc, funding), func(c *gin.Context) { c.Status(204) })
	return engine
}

func TestBillingAdmissionGateOrder(t *testing.T) {
	db := new(admissionDB)
	engine := admissionEngine(db, true)
	for _, tc := range []struct {
		name     string
		row      admissionRow
		status   int
		contains string
	}{
		{"missing balance", admissionRow{missing: true, keySpent: 100}, 402, "depleted"},
		{"zero balance", admissionRow{keySpent: 100}, 402, "depleted"},
		{"override skips balance and monthly", admissionRow{override: true, missing: true, orgSpent: 100}, 204, ""},
		{"override still checks key first", admissionRow{override: true, keySpent: 100, taskSpent: 100}, 402, "key_spend_cap_reached"},
		{"override still checks task before session", admissionRow{override: true, taskSpent: 100, sessionSpent: 100}, 402, `"scope":"task"`},
		{"session before monthly", admissionRow{balance: 1, sessionSpent: 100, orgSpent: 100}, 402, `"scope":"session"`},
		{"monthly", admissionRow{balance: 1, orgSpent: 100}, 402, "monthly"},
		{"database failure", admissionRow{err: errors.New("read failed")}, 503, "billing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db.row = tc.row
			before := db.reads
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest("GET", "/v1/messages", nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), tc.contains)
			require.Equal(t, 1, db.reads-before)
		})
	}
}
