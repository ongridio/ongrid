package report

import (
	"context"
	"time"

	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	managerbizreport "github.com/ongridio/ongrid/internal/manager/biz/report"
	managermodelalert "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

type reportChannelGetter interface {
	GetChannelByID(context.Context, uint64) (*managermodelalert.Channel, error)
}

// NotificationDeliverer implements bizreport.Deliverer over the alert
// channel store + notify router, so biz/report stays free of the
// notify / alert imports. For each channel id it loads the
// notification_channels row, builds the matching notify.Sender
// (BuildSenderFromChannel — same path the alert notifier uses), and
// sends the report summary as a markdown message. v1 ships the
// markdown-text form across every channel type; a Feishu interactive
// card is a future enhancement that would extend notify.Sender.
type NotificationDeliverer struct {
	channels reportChannelGetter
	router   *notify.Router
}

func (s NotificationDeliverer) Deliver(ctx context.Context, summary managerbizreport.DeliverySummary, channelIDs []uint64) []managerbizreport.DeliveryRecord {
	out := make([]managerbizreport.DeliveryRecord, 0, len(channelIDs))
	for _, id := range channelIDs {
		rec := managerbizreport.DeliveryRecord{ChannelID: id, SentAt: time.Now().UTC()}
		ch, err := s.channels.GetChannelByID(ctx, id)
		if err != nil {
			rec.Status = "failed"
			rec.Error = "channel not found"
			out = append(out, rec)
			continue
		}
		rec.ChannelType = ch.ChannelType
		if !ch.Enabled {
			rec.Status = "failed"
			rec.Error = "channel disabled"
			out = append(out, rec)
			continue
		}
		sender, err := managerbizalert.BuildSenderFromChannel(ch)
		if err != nil {
			rec.Status = "failed"
			rec.Error = err.Error()
			out = append(out, rec)
			continue
		}
		msg := notify.Message{
			Subject:    summary.Title,
			Body:       summary.MarkdownSummary(),
			Severity:   notify.SeverityInfo,
			Source:     "report",
			OccurredAt: time.Now().UTC(),
		}
		if err := s.router.SendVia(ctx, msg, sender); err != nil {
			rec.Status = "failed"
			rec.Error = err.Error()
		} else {
			rec.Status = "sent"
		}
		out = append(out, rec)
	}
	return out
}

func NewNotificationDeliverer(channels reportChannelGetter, router *notify.Router) NotificationDeliverer {
	return NotificationDeliverer{channels: channels, router: router}
}
