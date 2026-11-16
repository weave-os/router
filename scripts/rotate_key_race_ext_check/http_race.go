package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type httpRotateRace struct {
	n      int
	repo   *postgres.Repository
	svc    *auth.Service
	engine *gin.Engine
}

func newHTTPRotateRace(pool *pgxpool.Pool) httpRotateRace {
	const n = 5
	const adminPassword = "rotate-race-http-check-password"

	repo := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	hold := newListHold(n)
	svc := auth.NewService(
		repo.Installations,
		&holdingAPIKeyRepo{inner: repo.APIKeys, hold: hold},
		repo.ExternalAPIKeys,
		repo.Users,
		auth.NoOpAPIKeyCache{},
		nil,
		time.Now,
	).WithAdminPassword(adminPassword)

	engine := gin.New()
	mgmt := engine.Group("/admin/v1", middleware.WithAdminOnly(svc))
	mgmt.POST("/keys/:id/rotate", admin.RotateAPIKeyHandler(svc))
	return httpRotateRace{n: n, repo: repo, svc: svc, engine: engine}
}

func checkHTTPRotateRace(ctx context.Context, race httpRotateRace) error {
	n, repo, svc, engine := race.n, race.repo, race.svc, race.engine

	install, err := svc.EnsureAdminInstallation(ctx)
	if err != nil {
		return fmt.Errorf("ensure admin installation: %w", err)
	}
	name := "http-race-key-" + uuid.NewString()[:8]
	issued, _, err := svc.IssueAPIKey(ctx, install.ID, &name, nil)
	if err != nil {
		return fmt.Errorf("issue key: %w", err)
	}
	fmt.Printf("method=httptest+WithAdminOnly installation_id=%s initial_key_id=%s\n", install.ID, issued.ID)

	session, _, err := svc.IssueAdminSession()
	if err != nil {
		return fmt.Errorf("issue admin session: %w", err)
	}

	var (
		ready    sync.WaitGroup
		done     sync.WaitGroup
		start    = make(chan struct{})
		okCount  atomic.Int64
		statusMu sync.Mutex
		statuses = map[int]int{}
	)
	ready.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/admin/v1/keys/"+issued.ID+"/rotate", nil)
			req.AddCookie(&http.Cookie{Name: auth.AdminSessionCookieName, Value: session, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			engine.ServeHTTP(rec, req)
			statusMu.Lock()
			statuses[rec.Code]++
			statusMu.Unlock()
			if rec.Code == http.StatusCreated {
				okCount.Add(1)
			}
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	keys, err := repo.APIKeys.ListForInstallation(ctx, install.ID)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	activeNamed := 0
	for _, k := range keys {
		if k.Name != nil && *k.Name == name {
			activeNamed++
		}
	}

	fmt.Printf("http_status_counts=%v\n", statuses)
	fmt.Printf("rotate_http_201=%d active_keys_with_race_name=%d\n", okCount.Load(), activeNamed)
	if okCount.Load() > 1 && activeNamed > 1 {
		fmt.Println("YES: race reproduces through gin WithAdminOnly + RotateAPIKeyHandler (httptest)")
	} else {
		fmt.Println("NO: HTTP path did not show multi-successor race")
	}
	return nil
}
