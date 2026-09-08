package store

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/alert"
	"github.com/ongridio/ongrid/internal/manager/biz/apm"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/promquery"
	"github.com/ongridio/ongrid/internal/pkg/promwrite"
)

// Real PromQL, persisted rules/incidents/delivery records and an HTTP receiver.
// Synthetic counters are confined to the isolated acceptance Prometheus.
func TestAPMAlertLifecycleIntegration(t *testing.T) {
	endpoint := os.Getenv("APM_TEST_PROMETHEUS")
	if endpoint == "" {
		t.Skip("run scripts/apm-test/run.sh")
	}
	ctx := t.Context()
	base := time.Now().Add(-25 * time.Minute).Truncate(30 * time.Second)
	service := fmt.Sprintf("apm-alert-%d", time.Now().UnixNano())
	var samples []promwrite.Sample
	for _, status := range []string{"200", "500"} {
		labels := []promwrite.Label{{Name: "__name__", Value: "http_server_request_duration_seconds_count"}, {Name: "service_name", Value: service}, {Name: "service_namespace", Value: "trade"}, {Name: "deployment_environment_name", Value: "acceptance"}, {Name: "http_response_status_code", Value: status}}
		sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name })
		var count float64
		for i := 0; i <= 40; i++ {
			if status == "200" {
				count += 30
			} else if i < 16 {
				count += 10
			}
			samples = append(samples, promwrite.Sample{Labels: labels, TsMs: base.Add(time.Duration(i) * 30 * time.Second).UnixMilli(), Value: count})
		}
	}
	if err := promwrite.New(endpoint, nil).Write(ctx, samples); err != nil {
		t.Fatal(err)
	}
	webhooks := make(chan notify.Message, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message notify.Message
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		webhooks <- message
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	repo := newTestRepo(t)
	config, err := json.Marshal(map[string]string{"endpoint": receiver.URL})
	if err != nil {
		t.Fatal(err)
	}
	channel := &model.Channel{Name: "apm-local-acceptance", ChannelType: model.ChannelTypeWebhook, Enabled: true, ConfigJSON: string(config)}
	if err := repo.CreateChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	uc := biz.NewUsecase(repo, nil)
	env, ns := "acceptance", "trade"
	template, err := apm.BuildAlertTemplate(apm.Query{Start: base, End: base.Add(7 * time.Minute), ServiceName: service, Environment: &env, ServiceNamespace: &ns, Protocol: "http"}, "error_rate", 5, 1, 60)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := uc.CreateRule(ctx, biz.RuleInput{RuleKey: "apm_acceptance", Name: "APM acceptance", Kind: model.RuleKindMetricRaw, ScopeType: "global", Severity: "warning", Enabled: true, Spec: map[string]any{"expr": template.Expr}, NotifyChannelIDs: []uint64{channel.ID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := uc.GetRule(ctx, rule.ID)
	if err != nil || stored.ConditionsJSON == "" {
		t.Fatalf("persist rule: %+v %v", stored, err)
	}
	cache := biz.NewCachedRulesProvider(repo, time.Second, nil)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	clock := base.Add(7 * time.Minute)
	resolver := biz.NewDBChannelResolver(repo, nil)
	resolver.SetRuleLookup(repo.GetRuleByKey)
	evaluator := biz.NewPipelineEvaluator(biz.PipelineEvaluatorOpts{Usecase: uc, Rules: cache, Resolver: resolver, Notifier: notify.NewRouter(true, time.Second, nil), PromQuerier: promquery.New(endpoint, nil), Cooldown: time.Hour, Now: func() time.Time { return clock }})
	evaluator.EvaluateOnce(ctx)
	incidents, err := uc.ListIncidents(ctx, biz.IncidentFilter{RuleKey: rule.RuleKey})
	if err != nil || len(incidents) != 1 {
		t.Fatalf("firing: %+v %v", incidents, err)
	}
	select {
	case message := <-webhooks:
		if message.Labels["service"] != service {
			t.Fatalf("lost service identity: %+v", message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("notification not delivered")
	}
	deliveries, err := repo.ListDeliveryRows(ctx, DeliveryFilter{})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("delivery records: %+v %v", deliveries, err)
	}
	clock = base.Add(18 * time.Minute)
	evaluator.EvaluateOnce(ctx)
	recovered, err := uc.GetIncident(ctx, incidents[0].ID)
	if err != nil || recovered.Status != model.IncidentStatusResolved {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	t.Log("APM rule saved and reloaded; real PromQL fired; local HTTP webhook received service identity; incident resolved after healthy samples")
}
