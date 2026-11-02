package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/card"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// approvalSender 使用应用凭证主动投递卡片，不依赖短期 sessionWebhook。
type approvalSender struct {
	app    *model.ImApp
	target *model.ApprovalTarget
	http   *http.Client
	base   string
}

func (s *approvalSender) SendText(ctx context.Context, _, _, text string) (string, error) {
	return s.SendApproval(ctx, bridge.ApprovalCard{Text: text})
}
func (s *approvalSender) SendApproval(ctx context.Context, c bridge.ApprovalCard) (string, error) {
	id := uuid.NewString()
	body := map[string]any{"cardTemplateId": s.app.ApprovalTemplateID, "outTrackId": id, "callbackType": "STREAM", "userIdType": 1, "cardData": dingCardData(c), "openSpaceId": s.target.DeliverySpace}
	if strings.HasPrefix(s.target.DeliverySpace, "dtv1.card//IM_GROUP.") {
		body["imGroupOpenDeliverModel"] = map[string]string{"robotCode": s.app.AppID}
		body["imGroupOpenSpaceModel"] = map[string]bool{"supportForward": false}
	} else if strings.HasPrefix(s.target.DeliverySpace, "dtv1.card//IM_ROBOT.") {
		body["imRobotOpenDeliverModel"] = map[string]string{"robotCode": s.app.AppID, "spaceType": "IM_ROBOT"}
		body["imRobotOpenSpaceModel"] = map[string]bool{"supportForward": false}
	} else {
		return "", fmt.Errorf("invalid dingtalk card destination")
	}
	err := s.call(ctx, http.MethodPost, "/v1.0/card/instances/createAndDeliver", body)
	return id, err
}
func (s *approvalSender) EditApproval(ctx context.Context, id string, c bridge.ApprovalCard) error {
	return s.call(ctx, http.MethodPut, "/v1.0/card/instances", map[string]any{"outTrackId": id, "userIdType": 1, "cardData": dingCardData(c), "cardUpdateOptions": map[string]bool{"updateCardDataByKey": true}})
}
func dingCardData(c bridge.ApprovalCard) map[string]any {
	return map[string]any{"cardParamMap": map[string]string{"text": c.PlainText(), "approve_token": c.Buttons.Approve, "reject_token": c.Buttons.Reject, "review_url": c.ReviewURL}}
}
func (s *approvalSender) request(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("x-acs-dingtalk-access-token", token)
	}
	res, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode == 429 {
		seconds, _ := strconv.Atoi(res.Header.Get("Retry-After"))
		return nil, &bridge.DeliveryError{Code: "rate_limited", RetryAfter: time.Duration(max(seconds, 1)) * time.Second}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("dingtalk card status=%d", res.StatusCode)
	}
	return raw, nil
}
func (s *approvalSender) call(ctx context.Context, method, path string, body any) error {
	raw, err := s.request(ctx, http.MethodPost, "/v1.0/oauth2/accessToken", "", map[string]string{"appKey": s.app.AppID, "appSecret": s.app.AppSecret})
	if err != nil {
		return err
	}
	var auth struct {
		Token string `json:"accessToken"`
	}
	if err = json.Unmarshal(raw, &auth); err != nil {
		return err
	}
	if auth.Token == "" {
		return fmt.Errorf("dingtalk card: missing access token")
	}
	raw, err = s.request(ctx, method, path, auth.Token, body)
	if err != nil {
		return err
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if !out.Success {
		return fmt.Errorf("dingtalk card request rejected")
	}
	return nil
}
func (c *StreamClient) onCard(parent context.Context, r *card.CardRequest) (*card.CardResponse, error) {
	if r == nil || r.UserIdType != 1 {
		return &card.CardResponse{}, nil
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	err := c.bridge.HandleApprovalCallback(ctx, bridge.ApprovalCallback{ConnectionIdentity: bridge.ConnectionIdentity(c.app), Provider: "dingtalk", AppIdentifier: c.app.AppID, ChatID: r.SpaceId, MessageID: r.OutTrackId, Subject: bridge.Subject{Scope: r.CorpId, User: r.UserId}, Token: r.GetActionString("ongrid_token")})
	// 不依据回调成功直接改全局审批状态，等待持久化协调器回写。
	note := bridge.ApprovalReceipt(err, c.app.DefaultLocale, "dingtalk")
	return &card.CardResponse{UserPrivateData: &card.CardDataDto{CardParamMap: map[string]string{"receipt": note}}}, nil
}
func (s senderAdapter) ApprovalDestination() (string, string) { return s.deliverySpace, s.callbackChat }
