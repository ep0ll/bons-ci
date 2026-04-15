package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metric descriptors for the BYOC platform.
// All metrics use the "byoc_" namespace prefix for clarity in shared scrape targets.
type Metrics struct {
	// RunnersActive tracks runners in provisioning | registering | idle | busy states.
	RunnersActive *prometheus.GaugeVec
	// RunnersIdle tracks runners currently waiting for a job (warm-pool).
	RunnersIdle *prometheus.GaugeVec
	// JobQueueDepth tracks jobs waiting because MaxRunners has been reached.
	JobQueueDepth *prometheus.GaugeVec
	// ProvisionLatency measures time from webhook receipt to runner registration.
	ProvisionLatency *prometheus.HistogramVec
	// WebhookTotal counts webhook events received, labelled by outcome.
	WebhookTotal *prometheus.CounterVec
	// RunnerTerminations counts runner termination events.
	RunnerTerminations *prometheus.CounterVec
	// APIRequestDuration measures HTTP handler latency.
	APIRequestDuration *prometheus.HistogramVec
	// RateLimitHits counts rate-limiter rejections per tenant.
	RateLimitHits *prometheus.CounterVec
}

// NewMetrics registers and returns all application metrics.
// Call once at startup; passing the same registry avoids duplicate registrations.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		RunnersActive: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "byoc_runners_active_total",
			Help: "Number of runners currently active (provisioning + registering + idle + busy).",
		}, []string{"tenant_id"}),

		RunnersIdle: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "byoc_runners_idle_total",
			Help: "Number of runners currently idle (warm-pool, waiting for a job).",
		}, []string{"tenant_id"}),

		JobQueueDepth: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "byoc_job_queue_depth",
			Help: "Number of GitHub jobs waiting because the tenant's runner limit is reached.",
		}, []string{"tenant_id"}),

		ProvisionLatency: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "byoc_provision_latency_seconds",
			Help:    "Latency from workflow_job.queued webhook to runner registered on GitHub.",
			Buckets: []float64{5, 10, 20, 30, 60, 90, 120, 180},
		}, []string{"tenant_id", "provisioner_type"}),

		WebhookTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "byoc_webhook_total",
			Help: "Total GitHub webhook events received.",
		}, []string{"event_type", "action", "status"}),

		RunnerTerminations: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "byoc_runner_terminations_total",
			Help: "Total runner termination events, labelled by reason.",
		}, []string{"tenant_id", "reason"}),

		APIRequestDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "byoc_api_request_duration_seconds",
			Help:    "HTTP API request duration.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "path", "status_code"}),

		RateLimitHits: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "byoc_rate_limit_hits_total",
			Help: "Number of times the per-tenant provisioning rate limiter rejected a request.",
		}, []string{"tenant_id"}),
	}
}
