package aiops

import (
	"context"
	"fmt"
	"time"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	managermodelalert "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

type notificationChannels interface {
	GetChannelByID(context.Context, uint64) (*managermodelalert.Channel, error)
	ListChannels(context.Context, managerbizalert.ChannelFilter) ([]*managermodelalert.Channel, error)
}

// NotificationSender implements aiopstools.NotificationSender (the
// send_notification tool seam)
// over the alert channel store + notify router — same BuildSenderFromChannel
// path the alert notifier / flow notify node use.
type NotificationSender struct {
	Channels notificationChannels
	Router   *notify.Router
}

func (s NotificationSender) ListNotificationChannels(ctx context.Context) ([]aiopstools.NotificationChannel, error) {
	chs, err := s.Channels.ListChannels(ctx, managerbizalert.ChannelFilter{})
	if err != nil {
		return nil, err
	}
	out := make([]aiopstools.NotificationChannel, 0, len(chs))
	for _, ch := range chs {
		if !ch.Enabled {
			continue
		}
		out = append(out, aiopstools.NotificationChannel{ID: ch.ID, Name: ch.Name, Kind: ch.ChannelType})
	}
	return out, nil
}

func (s NotificationSender) SendNotification(ctx context.Context, channelID uint64, title, text string) error {
	ch, err := s.Channels.GetChannelByID(ctx, channelID)
	if err != nil {
		return fmt.Errorf("channel %d: not found", channelID)
	}
	if !ch.Enabled {
		return fmt.Errorf("channel %q: disabled", ch.Name)
	}
	sender, err := managerbizalert.BuildSenderFromChannel(ch)
	if err != nil {
		return err
	}
	msg := notify.Message{
		Subject:    title,
		Body:       text,
		Severity:   notify.SeverityInfo,
		Source:     "assistant",
		OccurredAt: time.Now().UTC(),
	}
	return s.Router.SendVia(ctx, msg, sender)
}
