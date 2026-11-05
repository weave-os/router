package admin

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/policy"
)

const drillDownSessionsPerPerson = 100

type dashboardDrillDownModel struct {
	RequestedModel       string   `json:"requested_model"`
	DecisionModel        string   `json:"decision_model"`
	Providers            []string `json:"providers"`
	CatalogCostUSD       float64  `json:"catalog_cost_usd"`
	ComparisonCostUSD    float64  `json:"comparison_cost_usd"`
	Requests             int64    `json:"requests"`
	SubscriptionRequests int64    `json:"subscription_requests"`
}

type dashboardDrillDownSession struct {
	SessionID  string                    `json:"session_id"`
	ClientApp  string                    `json:"client_app"`
	StartedAt  time.Time                 `json:"started_at"`
	FinishedAt time.Time                 `json:"finished_at"`
	Models     []dashboardDrillDownModel `json:"models"`
}

type dashboardDrillDownPerson struct {
	ID          string                      `json:"id"`
	Linked      bool                        `json:"linked"`
	DisplayName string                      `json:"display_name"`
	Email       string                      `json:"email"`
	Sessions    []dashboardDrillDownSession `json:"sessions"`
}

type dashboardDrillDownResponse struct {
	ComparisonModel string                     `json:"comparison_model"`
	People          []dashboardDrillDownPerson `json:"people"`
}

// MetricsDrillDownHandler lists the people, sessions and model pairs behind a
// dashboard data point; `group`+`key` narrow to the clicked series.
func MetricsDrillDownHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		group := dashboardMetricGroup(c.Query("group"))
		billing := c.Query("billing")
		if billing != "" && billing != billingAPI && billing != billingSubscription {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Unsupported billing filter."})
			return
		}
		from, to := parseTimeWindow(c)
		installationID, ok := dashboardScope(c)
		if !ok {
			return
		}
		rows, err := proxySvc.MetricsDrillDown(c.Request.Context(), installationID, from, to)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch drill-down."})
			return
		}
		var models []string
		if raw := c.Query("models"); raw != "" {
			models = strings.Split(raw, ",")
		}
		rows = slices.DeleteFunc(rows, func(row proxy.TelemetryDrillDownRow) bool {
			return !drillDownMatches(row, group, c.Query("key"), models, billing, c.Query("routing_state"))
		})
		c.JSON(http.StatusOK, dashboardDrillDownResponse{
			ComparisonModel: policy.SavingsComparisonModelLabel,
			People:          drillDownPeople(rows),
		})
	}
}

func drillDownMatches(row proxy.TelemetryDrillDownRow, group dashboardMetricGroup, key string, models []string, billing, routingState string) bool {
	if len(models) > 0 && !slices.Contains(models, row.DecisionModel) {
		return false
	}
	if billing != "" && *factGroup(row.TelemetryDashboardFact, dashboardGroupBilling) != billing {
		return false
	}
	if routingState != "" && *factGroup(row.TelemetryDashboardFact, dashboardGroupRoutingState) != routingState {
		return false
	}
	if key == "" {
		return true
	}
	if group == dashboardGroupUserLink {
		return (key == userLinkLinked) == row.Linked
	}
	label := factGroup(row.TelemetryDashboardFact, group)
	return label == nil || *label == key
}

func drillDownPeople(rows []proxy.TelemetryDrillDownRow) []dashboardDrillDownPerson {
	people := map[string]*dashboardDrillDownPerson{}
	sessions := map[[2]string]*dashboardDrillDownSession{}
	var order []string
	for _, row := range rows {
		person, ok := people[row.PersonID]
		if !ok {
			person = &dashboardDrillDownPerson{ID: row.PersonID, Linked: row.Linked, DisplayName: row.DisplayName, Email: row.Email}
			people[row.PersonID] = person
			order = append(order, row.PersonID)
		}
		sessionKey := [2]string{row.PersonID, row.SessionID}
		session, ok := sessions[sessionKey]
		if !ok {
			session = &dashboardDrillDownSession{SessionID: row.SessionID, ClientApp: row.ClientApp, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt}
			sessions[sessionKey] = session
		}
		session.StartedAt = minTime(session.StartedAt, row.StartedAt)
		session.FinishedAt = maxTime(session.FinishedAt, row.FinishedAt)
		var subscriptionRequests int64
		if row.SubscriptionServed {
			subscriptionRequests = row.RequestCount
		}
		session.Models = append(session.Models, dashboardDrillDownModel{
			RequestedModel:       row.RequestedModel,
			DecisionModel:        row.DecisionModel,
			Providers:            row.Providers,
			CatalogCostUSD:       billedCost(row.TelemetryDashboardFact),
			ComparisonCostUSD:    comparisonCost(row.TelemetryDashboardFact),
			Requests:             row.RequestCount,
			SubscriptionRequests: subscriptionRequests,
		})
	}
	for key, session := range sessions {
		people[key[0]].Sessions = append(people[key[0]].Sessions, *session)
	}
	out := make([]dashboardDrillDownPerson, 0, len(order))
	for _, id := range order {
		person := people[id]
		slices.SortFunc(person.Sessions, func(a, b dashboardDrillDownSession) int {
			return b.StartedAt.Compare(a.StartedAt)
		})
		if len(person.Sessions) > drillDownSessionsPerPerson {
			person.Sessions = person.Sessions[:drillDownSessionsPerPerson]
		}
		out = append(out, *person)
	}
	slices.SortFunc(out, func(a, b dashboardDrillDownPerson) int {
		return cmp.Compare(personCost(b), personCost(a))
	})
	return out
}

func personCost(person dashboardDrillDownPerson) float64 {
	var total float64
	for _, session := range person.Sessions {
		for _, model := range session.Models {
			total += model.CatalogCostUSD
		}
	}
	return total
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
