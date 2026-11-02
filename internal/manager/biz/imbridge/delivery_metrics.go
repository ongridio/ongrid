package imbridge

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"time"
)

type DeliveryStats struct {
	Provider string
	Backlog  int64
	Oldest   *time.Time
}
type DeliveryStatsReader interface {
	ApprovalDeliveryStats(context.Context) ([]DeliveryStats, error)
}
type DeliveryMetrics struct {
	attempts *prometheus.CounterVec
	backlog  *prometheus.GaugeVec
	oldest   *prometheus.GaugeVec
}

func NewDeliveryMetrics(reg prometheus.Registerer) (*DeliveryMetrics, error) {
	m := &DeliveryMetrics{attempts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ongrid_im_approval_deliveries_total", Help: "IM approval delivery attempts by outcome."}, []string{"provider", "outcome"}), backlog: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ongrid_im_approval_backlog", Help: "Undelivered or failed approval notifications."}, []string{"provider"}), oldest: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ongrid_im_approval_oldest_seconds", Help: "Age of the oldest notification awaiting delivery."}, []string{"provider"})}
	for _, c := range []prometheus.Collector{m.attempts, m.backlog, m.oldest} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}
func (m *DeliveryMetrics) refresh(ctx context.Context, r ApprovalDeliveryRepo) error {
	reader, ok := r.(DeliveryStatsReader)
	if !ok {
		return nil
	}
	rows, err := reader.ApprovalDeliveryStats(ctx)
	if err != nil {
		return err
	}
	for _, p := range []string{"slack", "telegram", "feishu", "dingtalk"} {
		m.backlog.WithLabelValues(p).Set(0)
		m.oldest.WithLabelValues(p).Set(0)
	}
	for _, row := range rows {
		switch row.Provider {
		case "slack", "telegram", "feishu", "dingtalk":
		default:
			continue
		}
		m.backlog.WithLabelValues(row.Provider).Set(float64(row.Backlog))
		if row.Oldest != nil {
			m.oldest.WithLabelValues(row.Provider).Set(max(0, time.Since(*row.Oldest).Seconds()))
		}
	}
	return nil
}
func (d *ApprovalDelivery) SetMetrics(m *DeliveryMetrics) { d.metrics = m }
