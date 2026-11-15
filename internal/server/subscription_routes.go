package server

import (
	"github.com/gin-gonic/gin"
	"time"
	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"
)

func registerInternalSubscriptions(engine *gin.Engine, group *gin.RouterGroup, authSvc *auth.Service, features Features, internalToken string) {
	if !authSvc.SubscriptionAccountsEnabled() {
		return
	}
	group.GET("/subscription-accounts/:subscriberID", admin.InternalListSubscriptionAccountsHandler(authSvc))
	group.PATCH("/subscription-accounts/:subscriberID/:accountID", admin.InternalUpdateSubscriptionAccountHandler(authSvc))
	group.DELETE("/subscription-accounts/:subscriberID/:accountID", admin.InternalDeleteSubscriptionAccountHandler(authSvc))
	if features.SubscriptionRefresh != nil {
		refreshGroup := engine.Group("/internal/v1", middleware.WithTimeout(time.Minute), middleware.WithInternalServiceAuth(internalToken))
		refreshGroup.POST("/subscription-accounts/:subscriberID/:accountID/refresh", admin.InternalRefreshSubscriptionAccountHandler(features.SubscriptionRefresh))
	}
	if features.SubscriptionLogins != nil {
		group.POST("/subscription-logins/:subscriberID", admin.InternalStartSubscriptionLoginHandler(authSvc, features.SubscriptionLogins))
		group.POST("/subscription-logins/:subscriberID/complete", admin.InternalCompleteSubscriptionLoginHandler(authSvc, features.SubscriptionLogins))
	}
}
