package admin

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/proxy"
)

// individualTopModels matches the hosted Individuals page's routed-models column.
const individualTopModels = 3

type dashboardActivityPoint struct {
	Time  int64   `json:"time"`
	Value float64 `json:"value"`
}

type dashboardIndividualModel struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
}

type dashboardIndividual struct {
	ID                  string                     `json:"id"`
	Linked              bool                       `json:"linked"`
	DisplayName         string                     `json:"display_name"`
	Email               string                     `json:"email"`
	LastRequestAt       *string                    `json:"last_request_at"`
	Requests            int64                      `json:"requests"`
	CostUSD             float64                    `json:"cost_usd"`
	InputTokens         int64                      `json:"input_tokens"`
	OutputTokens        int64                      `json:"output_tokens"`
	CacheReadTokens     int64                      `json:"cache_read_tokens"`
	CacheCreationTokens int64                      `json:"cache_creation_tokens"`
	PassthroughRequests int64                      `json:"passthrough_requests"`
	RequestActivity     []dashboardActivityPoint   `json:"request_activity"`
	CostActivity        []dashboardActivityPoint   `json:"cost_activity"`
	PassthroughActivity []dashboardActivityPoint   `json:"passthrough_activity"`
	Models              []dashboardIndividualModel `json:"models"`
	OtherRequests       int64                      `json:"other_requests"`
}

type dashboardIndividualsResponse struct {
	Individuals []dashboardIndividual `json:"individuals"`
}

// MetricsIndividualsHandler serves each person's usage totals, per-bucket
// activity and routed-model mix over the window, plus everyone who can route
// but sent nothing in it.
func MetricsIndividualsHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		granularity := c.DefaultQuery("granularity", "day")
		if granularity != "hour" && granularity != "day" && granularity != "week" {
			granularity = "day"
		}
		from, to := parseTimeWindow(c)
		installationID, ok := dashboardScope(c)
		if !ok {
			return
		}
		ctx := c.Request.Context()
		facts, err := proxySvc.MetricsIndividualFacts(ctx, installationID, from, to, granularity)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch individuals."})
			return
		}
		people, err := proxySvc.MetricsPeople(ctx, installationID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch people."})
			return
		}
		c.JSON(http.StatusOK, dashboardIndividualsResponse{Individuals: dashboardIndividuals(facts, people)})
	}
}

type individualActivity struct {
	requests, cost, passthrough map[int64]float64
	models                      map[string]int64
}

func dashboardIndividuals(facts []proxy.TelemetryIndividualFact, people []proxy.TelemetryPerson) []dashboardIndividual {
	byID := map[string]*dashboardIndividual{}
	activity := map[string]*individualActivity{}
	individualFor := func(id string, linked bool, displayName, email string) *dashboardIndividual {
		if existing, ok := byID[id]; ok {
			return existing
		}
		created := &dashboardIndividual{ID: id, Linked: linked, DisplayName: displayName, Email: email}
		byID[id] = created
		activity[id] = &individualActivity{
			requests: map[int64]float64{}, cost: map[int64]float64{}, passthrough: map[int64]float64{}, models: map[string]int64{},
		}
		return created
	}
	for _, person := range people {
		individual := individualFor(person.ID, person.Linked, person.DisplayName, person.Email)
		if person.LastRequestAt != nil {
			formatted := person.LastRequestAt.UTC().Format(time.RFC3339)
			individual.LastRequestAt = &formatted
		}
	}
	for _, fact := range facts {
		individual := individualFor(fact.PersonID, fact.Linked, fact.DisplayName, fact.Email)
		usage := activity[fact.PersonID]
		bucket := fact.Bucket.UnixMilli()
		cost := billedCost(fact.TelemetryDashboardFact)
		individual.Requests += fact.RequestCount
		individual.CostUSD += cost
		individual.InputTokens += fact.InputTokens
		individual.OutputTokens += fact.OutputTokens
		individual.CacheReadTokens += fact.CacheReadTokens
		individual.CacheCreationTokens += fact.CacheCreationTokens
		usage.requests[bucket] += float64(fact.RequestCount)
		usage.cost[bucket] += cost
		switch dashboardDecisionType(fact.DecisionType) {
		case dashboardDecisionPassthrough:
			individual.PassthroughRequests += fact.RequestCount
			usage.passthrough[bucket] += float64(fact.RequestCount)
		case dashboardDecisionRouterPick:
			usage.models[fact.DecisionModel] += fact.RequestCount
		}
	}
	out := make([]dashboardIndividual, 0, len(byID))
	for id, individual := range byID {
		usage := activity[id]
		individual.RequestActivity = activityPoints(usage.requests)
		individual.CostActivity = activityPoints(usage.cost)
		individual.PassthroughActivity = activityPoints(usage.passthrough)
		individual.Models, individual.OtherRequests = topModels(usage.models)
		out = append(out, *individual)
	}
	slices.SortFunc(out, func(a, b dashboardIndividual) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func activityPoints(values map[int64]float64) []dashboardActivityPoint {
	points := make([]dashboardActivityPoint, 0, len(values))
	for bucket, value := range values {
		points = append(points, dashboardActivityPoint{Time: bucket, Value: value})
	}
	slices.SortFunc(points, func(a, b dashboardActivityPoint) int { return cmp.Compare(a.Time, b.Time) })
	return points
}

// topModels keeps the most-requested routed models and sums the rest.
func topModels(requestsByModel map[string]int64) ([]dashboardIndividualModel, int64) {
	models := make([]dashboardIndividualModel, 0, len(requestsByModel))
	for model, requests := range requestsByModel {
		models = append(models, dashboardIndividualModel{Model: model, Requests: requests})
	}
	slices.SortFunc(models, func(a, b dashboardIndividualModel) int {
		return cmp.Or(cmp.Compare(b.Requests, a.Requests), cmp.Compare(a.Model, b.Model))
	})
	if len(models) <= individualTopModels {
		return models, 0
	}
	var other int64
	for _, model := range models[individualTopModels:] {
		other += model.Requests
	}
	return models[:individualTopModels], other
}
