package imbridge

import (
	"context"
	"fmt"

	managerbizimbridgefeishu "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/feishu"
	managerbizimbridgeslack "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/slack"
	managerbizimbridgetelegram "github.com/ongridio/ongrid/internal/manager/biz/imbridge/provider/telegram"
	managerimbridgemodel "github.com/ongridio/ongrid/internal/manager/model/imbridge"
)

// IMMessageSender sends to an explicit group through an IM Bridge app.
// Direct group sends are available only for providers with a stable outbound
// group API; DingTalk's current integration can only reply to an inbound
// session webhook and therefore rejects arbitrary group sends.
type IMMessageSender struct {
	Apps imAppGetter
}

type imAppGetter interface {
	GetApp(ctx context.Context, id uint64) (*managerimbridgemodel.ImApp, error)
}

func (s IMMessageSender) SendIMGroupMessage(ctx context.Context, imAppID uint64, groupID, text string) error {
	app, err := s.Apps.GetApp(ctx, imAppID)
	if err != nil {
		return fmt.Errorf("get IM app %d: %w", imAppID, err)
	}
	if app == nil {
		return fmt.Errorf("IM app %d: not found", imAppID)
	}
	if !app.Enabled {
		return fmt.Errorf("IM app %q is disabled", app.Name)
	}
	switch app.Provider {
	case managerimbridgemodel.ProviderFeishu:
		_, err = managerbizimbridgefeishu.NewClient(app.AppID, app.AppSecret).SendText(ctx, groupID, "chat_id", text)
	case managerimbridgemodel.ProviderTelegram:
		_, err = managerbizimbridgetelegram.NewClient(app.AppSecret).SendMessage(ctx, groupID, text)
	case managerimbridgemodel.ProviderSlack:
		client, clientErr := managerbizimbridgeslack.NewClientFromSecret(app.AppSecret)
		if clientErr != nil {
			return fmt.Errorf("build Slack IM client: %w", clientErr)
		}
		_, err = client.PostMessage(ctx, groupID, text)
	case managerimbridgemodel.ProviderDingTalk:
		return fmt.Errorf("DingTalk does not support direct group sends yet; its current IM Bridge integration only replies to inbound session webhooks")
	default:
		return fmt.Errorf("unsupported IM provider %q", app.Provider)
	}
	if err != nil {
		return fmt.Errorf("send through %s app %q: %w", app.Provider, app.Name, err)
	}
	return nil
}
