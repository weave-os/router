package admin

import (
	"net/http"
	"slices"
	"sort"
	"time"

	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"

	"github.com/gin-gonic/gin"
)

type dashboardMetricType string

const (
	dashboardMetricActiveUsers       dashboardMetricType = "active_users"
	dashboardMetricAPIListPriceSpend dashboardMetricType = "api_list_price_spend"
	dashboardMetricCost              dashboardMetricType = "cost"
	dashboardMetricModelSpend        dashboardMetricType = "model_spend"
	dashboardMetricModelUsage        dashboardMetricType = "model_usage"
)

type dashboardMetricGroup string

const (
	dashboardGroupNone         dashboardMetricGroup = ""
	dashboardGroupBilling      dashboardMetricGroup = "billing"
	dashboardGroupCostType     dashboardMetricGroup = "cost_type"
	dashboardGroupDecisionType dashboardMetricGroup = "decision_type"
	dashboardGroupModel        dashboardMetricGroup = "model"
	dashboardGroupRoutingState dashboardMetricGroup = "routing_state"
	dashboardGroupUserLink     dashboardMetricGroup = "user_link"
)

type dashboardDecisionType string

const (
	dashboardDecisionPassthrough dashboardDecisionType = "passthrough"
	dashboardDecisionRouterPick  dashboardDecisionType = "router_pick"
)

// Group labels match the hosted dashboard so shared cards read both hosts alike.
const (
	costTypeActual       = "Actual cost"
	costTypeComparison   = "Expensive model cost"
	billingSubscription  = "subscription"
	billingAPI           = "api"
	routingStateRouted   = "routed"
	routingStatePassthru = "passthrough"
	userLinkLinked       = "linked"
	userLinkUnlinked     = "unlinked"
)

// totalGranularity collapses the window into one value per group.
const totalGranularity = "total"

type dashboardPoint struct {
	Time  *int64  `json:"time"`
	Group *string `json:"group"`
	Value float64 `json:"value"`
}

type dashboardSeriesResponse struct {
	Values []dashboardPoint `json:"values"`
}

type dashboardReportSummaryResponse struct {
	RequestCount      int64   `json:"request_count"`
	TotalTokens       int64   `json:"total_tokens"`
	TotalCostUSD      float64 `json:"total_cost_usd"`
	ComparisonModel   string  `json:"comparison_model"`
	ComparisonCostUSD float64 `json:"comparison_cost_usd"`
	SavingsUSD        float64 `json:"savings_usd"`
	LastRequestAt     *string `json:"last_request_at"`
}

// MetricsSeriesHandler serves one dashboard metric per bucket, optionally grouped.
func MetricsSeriesHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		metric := dashboardMetricType(c.Query("type"))
		group := dashboardMetricGroup(c.Query("group"))
		if !validDashboardSeries(metric, group) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Unsupported metric type or group."})
			return
		}
		billing := c.Query("billing")
		if billing != "" && (metric == dashboardMetricActiveUsers || (billing != billingAPI && billing != billingSubscription)) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Unsupported billing filter."})
			return
		}
		granularity := c.DefaultQuery("granularity", "day")
		total := granularity == totalGranularity
		if total || (granularity != "hour" && granularity != "day" && granularity != "week") {
			granularity = "day"
		}
		from, to := parseTimeWindow(c)
		installationID, ok := dashboardScope(c)
		if !ok {
			return
		}

		ctx := c.Request.Context()
		var points []dashboardPoint
		if metric == dashboardMetricActiveUsers {
			bucket := granularity
			if total {
				bucket = totalGranularity
			}
			users, err := proxySvc.MetricsActiveUsers(ctx, installationID, from, to, bucket)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch active users."})
				return
			}
			points = activeUserPoints(users, group, total)
		} else {
			facts, err := proxySvc.MetricsDashboardFacts(ctx, installationID, from, to, granularity)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch metric series."})
				return
			}
			points = factPoints(factsInGroup(facts, dashboardGroupBilling, billing), metric, group, total)
		}
		c.JSON(http.StatusOK, dashboardSeriesResponse{Values: points})
	}
}

// MetricsReportSummaryHandler serves the dashboard's stat-card totals.
func MetricsReportSummaryHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		from, to := parseTimeWindow(c)
		installationID, ok := dashboardScope(c)
		if !ok {
			return
		}
		routingState := c.Query("routing_state")
		if routingState != "" && routingState != routingStateRouted && routingState != routingStatePassthru {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Unsupported routing_state."})
			return
		}
		facts, err := proxySvc.MetricsDashboardFacts(c.Request.Context(), installationID, from, to, "hour")
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch summary."})
			return
		}
		c.JSON(http.StatusOK, reportSummary(factsInGroup(facts, dashboardGroupRoutingState, routingState)))
	}
}

type dashboardPerson struct {
	ID            string  `json:"id"`
	Linked        bool    `json:"linked"`
	DisplayName   string  `json:"display_name"`
	Email         string  `json:"email"`
	LastRequestAt *string `json:"last_request_at"`
}

type dashboardPeopleResponse struct {
	People []dashboardPerson `json:"people"`
}

// MetricsPeopleHandler serves the adoption and inactive cards: everyone who
// can route, independent of the date filter.
func MetricsPeopleHandler(proxySvc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installationID, ok := dashboardScope(c)
		if !ok {
			return
		}
		people, err := proxySvc.MetricsPeople(c.Request.Context(), installationID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch people."})
			return
		}
		peopleResponse := make([]dashboardPerson, 0, len(people))
		for _, person := range people {
			row := dashboardPerson{ID: person.ID, Linked: person.Linked, DisplayName: person.DisplayName, Email: person.Email}
			if person.LastRequestAt != nil {
				formatted := person.LastRequestAt.UTC().Format(time.RFC3339)
				row.LastRequestAt = &formatted
			}
			peopleResponse = append(peopleResponse, row)
		}
		c.JSON(http.StatusOK, dashboardPeopleResponse{People: peopleResponse})
	}
}

func dashboardScope(c *gin.Context) (installationID string, ok bool) {
	allInstallations, installationID, ok := metricsScope(c)
	if allInstallations {
		return "", ok
	}
	return installationID, ok
}

func validDashboardSeries(metric dashboardMetricType, group dashboardMetricGroup) bool {
	switch metric {
	case dashboardMetricActiveUsers:
		return group == dashboardGroupNone || group == dashboardGroupUserLink
	case dashboardMetricCost:
		return group == dashboardGroupNone || group == dashboardGroupCostType || group == dashboardGroupRoutingState
	case dashboardMetricModelUsage, dashboardMetricModelSpend, dashboardMetricAPIListPriceSpend:
		switch group {
		case dashboardGroupNone, dashboardGroupModel, dashboardGroupBilling, dashboardGroupDecisionType, dashboardGroupRoutingState:
			return true
		}
	}
	return false
}

// factsInGroup keeps the facts whose group label is label; "" keeps every fact.
func factsInGroup(facts []proxy.TelemetryDashboardFact, group dashboardMetricGroup, label string) []proxy.TelemetryDashboardFact {
	if label == "" {
		return facts
	}
	return slices.DeleteFunc(facts, func(fact proxy.TelemetryDashboardFact) bool {
		return *factGroup(fact, group) != label
	})
}

func reportSummary(facts []proxy.TelemetryDashboardFact) dashboardReportSummaryResponse {
	summary := dashboardReportSummaryResponse{ComparisonModel: policy.SavingsComparisonModelLabel}
	var last time.Time
	for _, fact := range facts {
		summary.RequestCount += fact.RequestCount
		summary.TotalTokens += fact.InputTokens + fact.OutputTokens
		summary.TotalCostUSD += billedCost(fact)
		summary.ComparisonCostUSD += comparisonCost(fact)
		if fact.Bucket.After(last) {
			last = fact.Bucket
		}
	}
	summary.SavingsUSD = summary.ComparisonCostUSD - summary.TotalCostUSD
	if !last.IsZero() {
		formatted := last.UTC().Format(time.RFC3339)
		summary.LastRequestAt = &formatted
	}
	return summary
}

// billedCost is zero for subscription turns: they carry no marginal spend.
func billedCost(fact proxy.TelemetryDashboardFact) float64 {
	if fact.SubscriptionServed {
		return 0
	}
	return fact.ActualCostUSD
}

// comparisonCost reprices Router-picked API turns at the comparison model; other
// API turns keep their recorded cost, so only the Router's own choices save.
func comparisonCost(fact proxy.TelemetryDashboardFact) float64 {
	if fact.SubscriptionServed {
		return 0
	}
	if dashboardDecisionType(fact.DecisionType) != dashboardDecisionRouterPick || fact.AutomaticAlias {
		return fact.ActualCostUSD
	}
	if cost, ok := catalogCost(policy.SavingsComparisonModel, fact); ok {
		return cost
	}
	return fact.ActualCostUSD
}

// listPriceCost prices every turn, subscription or not, at its served model's list price.
func listPriceCost(fact proxy.TelemetryDashboardFact) float64 {
	if cost, ok := catalogCost(fact.DecisionModel, fact); ok {
		return cost
	}
	return fact.ActualCostUSD
}

func catalogCost(modelID string, fact proxy.TelemetryDashboardFact) (float64, bool) {
	model, ok := catalog.ByID(modelID)
	if !ok || len(model.Providers) == 0 {
		return 0, false
	}
	binding := model.Providers[0]
	provider := string(binding.Provider)
	input, cacheCreation, cacheRead := int(fact.InputTokens), int(fact.CacheCreationTokens), int(fact.CacheReadTokens)
	inputCost := catalog.EffectiveInputCost(input, cacheCreation, cacheRead, binding.Price, provider, catalog.UsageModifiers{})
	prompt := catalog.PromptTokens(input, cacheCreation, cacheRead, provider)
	return inputCost + catalog.EffectiveOutputCost(prompt, int(fact.OutputTokens), binding.Price, catalog.UsageModifiers{}), true
}

func factGroup(fact proxy.TelemetryDashboardFact, group dashboardMetricGroup) *string {
	var label string
	switch group {
	case dashboardGroupModel:
		label = fact.DecisionModel
	case dashboardGroupBilling:
		label = billingAPI
		if fact.SubscriptionServed {
			label = billingSubscription
		}
	case dashboardGroupDecisionType:
		label = fact.DecisionType
	case dashboardGroupRoutingState:
		label = routingStateRouted
		if dashboardDecisionType(fact.DecisionType) == dashboardDecisionPassthrough {
			label = routingStatePassthru
		}
	default:
		return nil
	}
	return &label
}

func factValue(fact proxy.TelemetryDashboardFact, metric dashboardMetricType) float64 {
	switch metric {
	case dashboardMetricModelUsage:
		return float64(fact.RequestCount)
	case dashboardMetricAPIListPriceSpend:
		return listPriceCost(fact)
	default:
		return billedCost(fact)
	}
}

func factPoints(facts []proxy.TelemetryDashboardFact, metric dashboardMetricType, group dashboardMetricGroup, total bool) []dashboardPoint {
	accumulator := newPointAccumulator(total)
	for _, fact := range facts {
		if metric == dashboardMetricCost && group == dashboardGroupCostType {
			actual, comparison := costTypeActual, costTypeComparison
			accumulator.add(fact.Bucket, &actual, billedCost(fact))
			accumulator.add(fact.Bucket, &comparison, comparisonCost(fact))
			continue
		}
		accumulator.add(fact.Bucket, factGroup(fact, group), factValue(fact, metric))
	}
	return accumulator.points()
}

func activeUserPoints(users []proxy.TelemetryActiveUsers, group dashboardMetricGroup, total bool) []dashboardPoint {
	accumulator := newPointAccumulator(total)
	for _, row := range users {
		var label *string
		if group == dashboardGroupUserLink {
			link := userLinkUnlinked
			if row.Linked {
				link = userLinkLinked
			}
			label = &link
		}
		accumulator.add(row.Bucket, label, float64(row.UserCount))
	}
	return accumulator.points()
}

type pointKey struct {
	time  int64
	group string
}

type pointAccumulator struct {
	total  bool
	order  []pointKey
	groups map[pointKey]*string
	values map[pointKey]float64
}

func newPointAccumulator(total bool) *pointAccumulator {
	return &pointAccumulator{total: total, groups: map[pointKey]*string{}, values: map[pointKey]float64{}}
}

func (a *pointAccumulator) add(bucket time.Time, group *string, value float64) {
	key := pointKey{}
	if !a.total {
		key.time = bucket.UnixMilli()
	}
	if group != nil {
		key.group = *group
	}
	if _, seen := a.values[key]; !seen {
		a.order = append(a.order, key)
		a.groups[key] = group
	}
	a.values[key] += value
}

func (a *pointAccumulator) points() []dashboardPoint {
	sort.SliceStable(a.order, func(i, j int) bool {
		if a.order[i].time != a.order[j].time {
			return a.order[i].time < a.order[j].time
		}
		return a.order[i].group < a.order[j].group
	})
	points := make([]dashboardPoint, 0, len(a.order))
	for _, key := range a.order {
		point := dashboardPoint{Group: a.groups[key], Value: a.values[key]}
		if !a.total {
			bucket := key.time
			point.Time = &bucket
		}
		points = append(points, point)
	}
	return points
}
