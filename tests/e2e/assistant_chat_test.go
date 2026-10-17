//go:build e2e

// Catalog: assistant chat — user-visible agent-loop contracts.
//
// These cases intentionally drive the public HTTP endpoints through a real
// manager process. Unit tests cover individual state transitions; this file
// protects the joins that previously regressed in production: session
// ownership, direct tool execution, progressive ToolSearch disclosure,
// streaming, semantic clarification, and human approval.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestAssistantChat_UserVisibleLoop(t *testing.T) {
	// Keep deferred schemas enabled even in the intentionally small E2E tool
	// registry. Production reaches this state once marketplace tools load; the
	// low threshold makes ToolSearch behaviour deterministic here.
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	admin := env.LoginAdmin()

	t.Run("direct answer persists root conversation and hides it from another user", func(t *testing.T) {
		env.FakeLLM().SetScript(testenv.LLMReply{Content: "E2E direct answer"})
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E direct chat")

		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "给我一个简短的健康摘要",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post message: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); got != "E2E direct answer" {
			t.Fatalf("assistant content=%q body=%v", got, body)
		}

		status, body, err = env.DoJSON("GET", chatMessagesPath(sessionID), nil, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("list messages: status=%d body=%v err=%v", status, body, err)
		}
		if got := int(body["total"].(float64)); got != 2 {
			t.Fatalf("message total=%d want 2; body=%v", got, body)
		}

		const (
			email    = "assistant-chat-other@ongrid.local"
			password = "E2E!assistant-chat-other"
		)
		mustCreateUser(t, env, admin.AccessToken, email, password, "user")
		other := env.Login(email, password)
		status, body, err = env.DoJSON("GET", chatMessagesPath(sessionID), nil, other.AccessToken)
		if err != nil || status != http.StatusNotFound {
			t.Fatalf("cross-user history: status=%d body=%v err=%v, want 404", status, body, err)
		}
	})

	t.Run("direct core tool closes the loop with real Prometheus evidence", func(t *testing.T) {
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "promql-1", Name: "query_promql", Arguments: `{"expr":"up"}`,
			}}},
			testenv.LLMReply{Content: "E2E PromQL evidence analyzed"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E PromQL")

		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "查询 up 指标",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post PromQL: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); got != "E2E PromQL evidence analyzed" {
			t.Fatalf("assistant content=%q body=%v", got, body)
		}
		if !containsToolCall(body["tool_calls"], "query_promql") {
			t.Fatalf("query_promql was not reported in tool_calls: %v", body)
		}
		requests := env.FakeLLM().Requests()
		if len(requests) != 2 || !containsString(requests[0].ToolNames, "query_promql") {
			t.Fatalf("LLM requests=%+v, want first turn to expose query_promql", requests)
		}
	})

	t.Run("ToolSearch reveals and executes a deferred tool", func(t *testing.T) {
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "search-1", Name: "ToolSearch", Arguments: `{"query":"select:query_alert_rules"}`,
			}}},
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "rules-1", Name: "query_alert_rules", Arguments: `{}`,
			}}},
			testenv.LLMReply{Content: "E2E alert rule inventory is empty"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E ToolSearch")

		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "列出当前告警规则",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post ToolSearch: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); got != "E2E alert rule inventory is empty" {
			t.Fatalf("assistant content=%q body=%v", got, body)
		}
		if !containsToolCall(body["tool_calls"], "ToolSearch") || !containsToolCall(body["tool_calls"], "query_alert_rules") {
			t.Fatalf("expected ToolSearch then query_alert_rules, body=%v", body)
		}
		requests := env.FakeLLM().Requests()
		if len(requests) != 3 || !containsString(requests[0].ToolNames, "ToolSearch") {
			t.Fatalf("LLM requests=%+v, want ToolSearch first", requests)
		}
	})

	t.Run("stream sends tool lifecycle and terminal summary", func(t *testing.T) {
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "stream-promql-1", Name: "query_promql", Arguments: `{"expr":"up"}`,
			}}},
			testenv.LLMReply{Content: "E2E streamed answer"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E streaming")
		events := postChatStream(t, env, admin.AccessToken, sessionID, "流式查询 up")
		for _, want := range []string{"assistant", "tool_start", "tool_end", "done", "summary"} {
			if !containsString(events, want) {
				t.Fatalf("SSE events=%v, missing %q", events, want)
			}
		}
	})

	t.Run("capture without an explicit target allows semantic clarification", func(t *testing.T) {
		for _, prompt := range []string{
			"抓 60 秒 tcp port 443 的包",
			"抓 HTTPS 流量，持续一分钟",
			"帮我抓一下数据包",
		} {
			t.Run(prompt, func(t *testing.T) {
				env.FakeLLM().SetScript(testenv.LLMReply{Content: "请先明确目标设备，再提出抓包方案。"})
				before := env.FakeLLM().CallCount()
				sessionID := createChatSession(t, env, admin.AccessToken, "E2E capture clarification")
				status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
					"content": prompt,
				}, admin.AccessToken)
				if err != nil || status != http.StatusOK {
					t.Fatalf("post capture clarification: status=%d body=%v err=%v", status, body, err)
				}
				if got := nestedString(body, "assistant_message", "content"); !strings.Contains(got, "设备") {
					t.Fatalf("clarification=%q, want explicit device question", got)
				}
				if after := env.FakeLLM().CallCount(); after != before+1 {
					t.Fatalf("clarification must use the semantic model: before=%d after=%d", before, after)
				}
				if calls, _ := body["tool_calls"].([]any); len(calls) != 0 {
					t.Fatalf("clarification executed tools: %v", calls)
				}
			})
		}
	})

	t.Run("named device is resolved through query_devices before host analysis", func(t *testing.T) {
		deviceID := env.SeedDevice(testenv.DeviceFixture{
			Name:      "edge-001",
			Hostname:  "VM-4-17-ubuntu",
			IPAddress: "10.20.30.41",
			Online:    true,
			Roles:     devicemodel.RoleBitServer,
		})
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "device-lookup-1", Name: "query_devices", Arguments: `{"name_contains":"edge-001","limit":5}`,
			}}},
			testenv.LLMReply{Content: "edge-001 在线，device_id 已解析，可以继续查看主机摘要。"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E named device")
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "看看 edge-001 的状态",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post named device: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); !strings.Contains(got, "device_id") {
			t.Fatalf("named-device answer=%q body=%v", got, body)
		}
		if !containsToolCall(body["tool_calls"], "query_devices") {
			t.Fatalf("query_devices was not reported for device %d: %v", deviceID, body)
		}
		requests := env.FakeLLM().Requests()
		if len(requests) != 2 || !containsString(requests[0].ToolNames, "query_devices") {
			t.Fatalf("LLM requests=%+v, want query_devices on first turn", requests)
		}
	})

	t.Run("ambiguous device name returns a choice instead of guessing a target", func(t *testing.T) {
		env.SeedDevice(testenv.DeviceFixture{Name: "api-1", Hostname: "api-1", Online: true, Roles: devicemodel.RoleBitServer})
		env.SeedDevice(testenv.DeviceFixture{Name: "api-2", Hostname: "api-2", Online: true, Roles: devicemodel.RoleBitServer})
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "device-lookup-ambiguous-1", Name: "query_devices", Arguments: `{"name_contains":"api","limit":10}`,
			}}},
			testenv.LLMReply{Content: "找到多个 api 相关设备，请选择 api-1 或 api-2 后我再继续。"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E ambiguous device")
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "看看 api 的负载",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post ambiguous device: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); !strings.Contains(got, "多个") || !strings.Contains(got, "选择") {
			t.Fatalf("ambiguous-device answer=%q body=%v", got, body)
		}
		if !containsToolCall(body["tool_calls"], "query_devices") {
			t.Fatalf("query_devices was not reported for ambiguity: %v", body)
		}
	})

	t.Run("missing host target for disk work allows semantic clarification", func(t *testing.T) {
		env.FakeLLM().SetScript(testenv.LLMReply{Content: "请先选择目标设备。"})
		before := env.FakeLLM().CallCount()
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E disk clarification")
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "磁盘快满了，帮我看看哪些目录最大",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post disk clarification: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); !strings.Contains(got, "目标设备") {
			t.Fatalf("disk clarification=%q, want target device question", got)
		}
		if after := env.FakeLLM().CallCount(); after != before+1 {
			t.Fatalf("disk clarification must use the semantic model: before=%d after=%d", before, after)
		}
		if calls, _ := body["tool_calls"].([]any); len(calls) != 0 {
			t.Fatalf("disk clarification executed tools: %v", calls)
		}
	})

	t.Run("ToolSearch no match settles after one lookup instead of looping", func(t *testing.T) {
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "search-missing-1", Name: "ToolSearch", Arguments: `{"query":"select:not_a_real_tool"}`,
			}}},
			testenv.LLMReply{Content: "当前环境没有这个能力，无法执行该操作。"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E missing tool")
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "使用一个不存在的工具",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post missing tool: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); got != "当前环境没有这个能力，无法执行该操作。" {
			t.Fatalf("missing-tool answer=%q body=%v", got, body)
		}
		if !containsToolCall(body["tool_calls"], "ToolSearch") {
			t.Fatalf("ToolSearch was not recorded: %v", body)
		}
		if got := len(env.FakeLLM().Requests()); got != 2 {
			t.Fatalf("LLM turns=%d want 2; ToolSearch no-match must not retry", got)
		}
	})

	t.Run("a failed read tool returns control to the same conversation", func(t *testing.T) {
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "bad-promql-1", Name: "query_promql", Arguments: `{}`,
			}}},
			testenv.LLMReply{Content: "查询参数缺少指标表达式；请提供具体 PromQL。"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E tool recovery")
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "查询一个指标",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post failing tool: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); got != "查询参数缺少指标表达式；请提供具体 PromQL。" {
			t.Fatalf("recovered answer=%q body=%v", got, body)
		}
		if !containsToolCall(body["tool_calls"], "query_promql") {
			t.Fatalf("failed query_promql was not recorded: %v", body)
		}
		if got := len(env.FakeLLM().Requests()); got != 2 {
			t.Fatalf("LLM turns=%d want 2 after one failed tool call", got)
		}
	})

	t.Run("stop interrupts an in-flight turn and leaves the session usable", func(t *testing.T) {
		block := env.FakeLLM().BlockNext()
		defer block.Release()
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E stop")
		result := make(chan int, 1)
		go func() {
			status, _, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
				"content": "请进行一次需要较长时间的分析",
			}, admin.AccessToken)
			if err != nil {
				result <- 0
				return
			}
			result <- status
		}()

		waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := block.WaitStarted(waitCtx); err != nil {
			t.Fatalf("LLM turn did not start: %v", err)
		}
		status, body, err := env.DoJSON("POST", "/api/v1/chat/sessions/"+sessionID+"/stop", nil, admin.AccessToken)
		if err != nil || status != http.StatusOK || body["stopped"] != true {
			t.Fatalf("stop session: status=%d body=%v err=%v", status, body, err)
		}
		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled turn did not return")
		}

		env.FakeLLM().SetScript(testenv.LLMReply{Content: "E2E turn after stop"})
		status, body, err = env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "取消后继续回答",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK || nestedString(body, "assistant_message", "content") != "E2E turn after stop" {
			t.Fatalf("post-stop retry: status=%d body=%v err=%v", status, body, err)
		}
	})

	t.Run("synchronous specialist work stays internal and returns to the root loop", func(t *testing.T) {
		status, setting, err := env.DoJSON("PUT", "/api/v1/system-settings/agent/write_enabled", map[string]any{
			"value":     "true",
			"sensitive": false,
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("enable delegation gate: status=%d body=%v err=%v", status, setting, err)
		}
		status, sessions, err := env.DoJSON("GET", "/api/v1/chat/sessions", nil, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("list sessions before delegation: status=%d body=%v err=%v", status, sessions, err)
		}
		before := int(sessions["total"].(float64))
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{
				ID: "delegate-1", Name: "AgentTool", Arguments: `{"description":"评估当前 SRE 风险","subagent_type":"specialist-sre","prompt":"检查当前告警和指标风险，返回简短证据。"}`,
			}}},
			testenv.LLMReply{Content: "E2E specialist evidence"},
			testenv.LLMReply{Content: "E2E root synthesized specialist result"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E delegated work")
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{
			"content": "请评估当前 SRE 风险并给我结论",
		}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post delegated work: status=%d body=%v err=%v", status, body, err)
		}
		if got := nestedString(body, "assistant_message", "content"); got != "E2E root synthesized specialist result" {
			t.Fatalf("delegated root answer=%q body=%v", got, body)
		}
		if !containsToolCall(body["tool_calls"], "AgentTool") {
			t.Fatalf("AgentTool was not reported in tool_calls: %v", body)
		}
		status, sessions, err = env.DoJSON("GET", "/api/v1/chat/sessions", nil, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("list sessions after delegation: status=%d body=%v err=%v", status, sessions, err)
		}
		if got := int(sessions["total"].(float64)); got != before+1 {
			t.Fatalf("visible sessions=%d want %d; internal worker leaked into chat list: %v", got, before+1, sessions)
		}
	})
}

func TestAssistantChat_OpsScenarioCoverage(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	admin := env.LoginAdmin()
	env.SeedDevice(testenv.DeviceFixture{
		Name:      "db-001",
		Hostname:  "db-001",
		IPAddress: "10.20.40.11",
		Online:    true,
		Roles:     devicemodel.RoleBitDatabase,
	})

	scripted := []struct {
		id           string
		title        string
		prompt       string
		script       []testenv.LLMReply
		wantTools    []string
		wantContains []string
	}{
		{
			id:     "O06",
			title:  "online database devices",
			prompt: "列出在线的数据库设备",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o06-devices", Name: "query_devices", Arguments: `{"role":"database","status":"online","limit":20}`}}},
				{Content: "在线数据库设备包括 db-001。"},
			},
			wantTools:    []string{"query_devices"},
			wantContains: []string{"db-001"},
		},
		{
			id:     "O16",
			title:  "direct promql query",
			prompt: "查询 up 指标并说明当前是否有样本",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o16-prom", Name: "query_promql", Arguments: `{"expr":"up"}`}}},
				{Content: "PromQL 查询已返回 up 指标样本。"},
			},
			wantTools:    []string{"query_promql"},
			wantContains: []string{"PromQL"},
		},
		{
			id:     "O17",
			title:  "metric catalog before database analysis",
			prompt: "看看 PostgreSQL 有哪些可用指标可以用于排障",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o17-catalog", Name: "list_metric_catalog", Arguments: `{"query":"postgresql","limit":20}`}}},
				{Content: "已查询指标目录；没有命中时不会编造 PostgreSQL 指标名。"},
			},
			wantTools:    []string{"list_metric_catalog"},
			wantContains: []string{"不会编造"},
		},
		{
			id:     "O21",
			title:  "incident inventory",
			prompt: "列出最近的告警事件",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o21-incidents", Name: "query_incidents", Arguments: `{"status":"open","limit":10}`}}},
				{Content: "当前没有需要展示的打开告警事件。"},
			},
			wantTools:    []string{"query_incidents"},
			wantContains: []string{"告警事件"},
		},
		{
			id:     "O24",
			title:  "change inventory",
			prompt: "最近有哪些配置或系统变更",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o24-changes", Name: "query_change_events", Arguments: `{"limit":10}`}}},
				{Content: "已查询最近变更；没有证据时不会归因到变更。"},
			},
			wantTools:    []string{"query_change_events"},
			wantContains: []string{"不会归因"},
		},
		{
			id:     "O25",
			title:  "topology impact",
			prompt: "看一下当前拓扑，判断有没有明显影响面",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o25-topology", Name: "get_topology", Arguments: `{}`}}},
				{Content: "已读取拓扑；只基于拓扑证据说明影响面。"},
			},
			wantTools:    []string{"get_topology"},
			wantContains: []string{"拓扑证据"},
		},
		{
			id:           "O28",
			title:        "packet path does not infer nat",
			prompt:       "这个包是不是经过 NAT 或网关了，帮我看一下链路",
			script:       []testenv.LLMReply{{Content: "不应进入模型；目标不明确时前置澄清。"}},
			wantContains: []string{"不能确定 NAT"},
		},
	}
	for _, tc := range scripted {
		t.Run(tc.id+" "+tc.title, func(t *testing.T) {
			env.FakeLLM().SetScript(tc.script...)
			sessionID := createChatSession(t, env, admin.AccessToken, "E2E "+tc.id)
			status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{"content": tc.prompt}, admin.AccessToken)
			if err != nil || status != http.StatusOK {
				t.Fatalf("%s post: status=%d body=%v err=%v", tc.id, status, body, err)
			}
			got := nestedString(body, "assistant_message", "content")
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("%s answer=%q missing %q; body=%v", tc.id, got, want, body)
				}
			}
			for _, toolName := range tc.wantTools {
				if !containsToolCall(body["tool_calls"], toolName) {
					t.Fatalf("%s missing tool %s in body=%v", tc.id, toolName, body)
				}
			}
		})
	}

	for _, tc := range []struct {
		id     string
		prompt string
		want   string
	}{
		{id: "O26", prompt: "DNS 解析失败了，帮我排查", want: "目标设备"},
		{id: "O30", prompt: "检查网络接口错误包", want: "目标设备"},
	} {
		t.Run(tc.id+" requires target", func(t *testing.T) {
			env.FakeLLM().SetScript(testenv.LLMReply{Content: "请先明确目标设备。"})
			before := env.FakeLLM().CallCount()
			sessionID := createChatSession(t, env, admin.AccessToken, "E2E "+tc.id)
			status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{"content": tc.prompt}, admin.AccessToken)
			if err != nil || status != http.StatusOK {
				t.Fatalf("%s post: status=%d body=%v err=%v", tc.id, status, body, err)
			}
			if got := nestedString(body, "assistant_message", "content"); !strings.Contains(got, tc.want) {
				t.Fatalf("%s clarification=%q missing %q; body=%v", tc.id, got, tc.want, body)
			}
			if after := env.FakeLLM().CallCount(); after != before+1 {
				t.Fatalf("%s clarification must use the semantic model: before=%d after=%d", tc.id, before, after)
			}
			if calls, _ := body["tool_calls"].([]any); len(calls) != 0 {
				t.Fatalf("%s clarification executed tools: %v", tc.id, calls)
			}
		})
	}
}

func TestAssistantChat_HostAndInventoryScenarioCoverage(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	admin := env.LoginAdmin()
	stale := time.Now().UTC().Add(-6 * time.Hour)
	opsID := env.SeedDevice(testenv.DeviceFixture{Name: "ops-001", Hostname: "ops-001", Online: true, Roles: devicemodel.RoleBitServer})
	env.SeedDevice(testenv.DeviceFixture{Name: "stale-001", Hostname: "stale-001", Online: false, Roles: devicemodel.RoleBitServer, LastSeen: &stale})
	env.SeedDevice(testenv.DeviceFixture{Name: "app-a", Hostname: "app-a", Online: true, Roles: devicemodel.RoleBitServer})
	env.SeedDevice(testenv.DeviceFixture{Name: "app-b", Hostname: "app-b", Online: true, Roles: devicemodel.RoleBitServer})
	status, setting, err := env.DoJSON("PUT", "/api/v1/system-settings/agent/write_enabled", map[string]any{
		"value":     "true",
		"sensitive": false,
	}, admin.AccessToken)
	if err != nil || status != http.StatusOK {
		t.Fatalf("enable delegation gate: status=%d body=%v err=%v", status, setting, err)
	}

	scenarios := []struct {
		id           string
		title        string
		prompt       string
		mentions     []map[string]string
		script       []testenv.LLMReply
		wantTools    []string
		wantContains []string
	}{
		{
			id:     "O04",
			title:  "nonexistent device is not guessed",
			prompt: "看看 no-such-edge 的状态",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o04-devices", Name: "query_devices", Arguments: `{"name_contains":"no-such-edge","limit":5}`}}},
				{Content: "没有找到 no-such-edge；请确认设备名称或使用 @ 选择设备。"},
			},
			wantTools:    []string{"query_devices"},
			wantContains: []string{"没有找到"},
		},
		{
			id:     "O07",
			title:  "stale device filter",
			prompt: "列出最近 5 分钟还在线的设备，不要把过期设备当在线",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o07-devices", Name: "query_devices", Arguments: `{"status":"online","last_seen_within_minutes":5,"limit":50}`}}},
				{Content: "已按最近 5 分钟过滤设备；过期设备不会算作当前在线。"},
			},
			wantTools:    []string{"query_devices"},
			wantContains: []string{"过期设备不会"},
		},
		{
			id:     "O08",
			title:  "compare mentioned device candidates",
			prompt: "比较 app-a 和 app-b 的风险，先确认设备",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o08-devices", Name: "query_devices", Arguments: `{"name_contains":"app","limit":10}`}}},
				{Content: "已找到 app-a 和 app-b；下一步应基于指标或主机证据比较。"},
			},
			wantTools:    []string{"query_devices"},
			wantContains: []string{"app-a", "app-b"},
		},
		{
			id:       "O09",
			title:    "host load returns to loop when edge data unavailable",
			prompt:   "看看这个设备 CPU 为什么高",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", opsID), "label": "ops-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o09-load", Name: "get_host_load", Arguments: fmt.Sprintf(`{"device_ids":[%d]}`, opsID)}}},
				{Content: "实时主机负载暂不可用，已回到对话并说明需要 edge 在线数据。"},
			},
			wantTools:    []string{"get_host_load"},
			wantContains: []string{"实时主机负载暂不可用"},
		},
		{
			id:       "O10",
			title:    "process list uses host process tool",
			prompt:   "找这个设备最占内存的进程",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", opsID), "label": "ops-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o10-proc", Name: "get_host_processes", Arguments: fmt.Sprintf(`{"device_ids":[%d],"top_n":5,"sort_by":"mem"}`, opsID)}}},
				{Content: "进程列表暂不可用，但工具失败后对话已正常收敛。"},
			},
			wantTools:    []string{"get_host_processes"},
			wantContains: []string{"对话已正常收敛"},
		},
		{
			id:     "O11",
			title:  "oom investigation starts with evidence",
			prompt: "最近是不是有 OOM，先查证据",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o11-prom", Name: "query_promql", Arguments: `{"expr":"increase(node_vmstat_oom_kill[1h])"}`}}},
				{Content: "已查询 OOM 指标；没有指标证据时不会直接下结论。"},
			},
			wantTools:    []string{"query_promql"},
			wantContains: []string{"不会直接下结论"},
		},
		{
			id:       "O13",
			title:    "large files are deferred and then selected",
			prompt:   "找这个设备 /var 下最大的文件",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", opsID), "label": "ops-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o13-search", Name: "ToolSearch", Arguments: `{"query":"select:host_find_large_files"}`}}},
				{ToolCalls: []testenv.LLMToolCall{{ID: "o13-files", Name: "host_find_large_files", Arguments: fmt.Sprintf(`{"device_id":%d,"paths":["/var"],"limit":10}`, opsID)}}},
				{Content: "已选择大文件工具；edge 数据不可用时不会编造文件列表。"},
			},
			wantTools:    []string{"ToolSearch", "host_find_large_files"},
			wantContains: []string{"不会编造"},
		},
		{
			id:       "O14",
			title:    "directory usage uses du summary",
			prompt:   "分析这个设备哪些目录占用最大",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", opsID), "label": "ops-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o14-search", Name: "ToolSearch", Arguments: `{"query":"select:host_du_summary"}`}}},
				{ToolCalls: []testenv.LLMToolCall{{ID: "o14-du", Name: "host_du_summary", Arguments: fmt.Sprintf(`{"device_id":%d,"paths":["/"],"depth":1}`, opsID)}}},
				{Content: "目录占用分析必须基于 host_du_summary 结果；当前没有 edge 数据。"},
			},
			wantTools:    []string{"ToolSearch", "host_du_summary"},
			wantContains: []string{"host_du_summary"},
		},
		{
			id:       "O15",
			title:    "file stat uses stat tool",
			prompt:   "确认这个设备 /var/log/syslog 是否存在",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", opsID), "label": "ops-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o15-search", Name: "ToolSearch", Arguments: `{"query":"select:host_stat_file"}`}}},
				{ToolCalls: []testenv.LLMToolCall{{ID: "o15-stat", Name: "host_stat_file", Arguments: fmt.Sprintf(`{"device_id":%d,"paths":["/var/log/syslog"]}`, opsID)}}},
				{Content: "文件存在性必须基于 stat 结果；当前无法从 edge 读取。"},
			},
			wantTools:    []string{"ToolSearch", "host_stat_file"},
			wantContains: []string{"stat"},
		},
		{
			id:     "O22",
			title:  "specific incident detail is deferred",
			prompt: "分析 incident 999 的详情",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o22-search", Name: "ToolSearch", Arguments: `{"query":"select:get_incident_detail"}`}}},
				{ToolCalls: []testenv.LLMToolCall{{ID: "o22-detail", Name: "get_incident_detail", Arguments: `{"incident_id":999}`}}},
				{Content: "incident 999 不存在，不能继续做根因推断。"},
			},
			wantTools:    []string{"ToolSearch", "get_incident_detail"},
			wantContains: []string{"不存在"},
		},
		{
			id:     "O23",
			title:  "deep incident analysis can delegate but returns to root",
			prompt: "对当前告警做一次深入 RCA",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o23-agent", Name: "AgentTool", Arguments: `{"description":"深入 RCA","subagent_type":"incident-investigator","prompt":"基于现有告警和指标做 RCA，返回证据和不确定性。"}`}}},
				{Content: "专家分析：没有足够证据。"},
				{Content: "根对话收到专家结果：没有足够证据，不生成确定根因。"},
			},
			wantTools:    []string{"AgentTool"},
			wantContains: []string{"根对话收到专家结果"},
		},
		{
			id:     "O29",
			title:  "network neighbors require inventory capability",
			prompt: "查一下交换机邻居关系",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o29-search", Name: "ToolSearch", Arguments: `{"query":"select:get_network_neighbors"}`}}},
				{Content: "当前网络资产能力未配置，不能编造交换机邻居关系。"},
			},
			wantTools:    []string{"ToolSearch"},
			wantContains: []string{"不能编造"},
		},
	}
	for _, tc := range scenarios {
		t.Run(tc.id+" "+tc.title, func(t *testing.T) {
			env.FakeLLM().SetScript(tc.script...)
			sessionID := createChatSession(t, env, admin.AccessToken, "E2E "+tc.id)
			payload := map[string]any{"content": tc.prompt}
			if len(tc.mentions) > 0 {
				payload["mentions"] = tc.mentions
			}
			status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), payload, admin.AccessToken)
			if err != nil || status != http.StatusOK {
				t.Fatalf("%s post: status=%d body=%v err=%v", tc.id, status, body, err)
			}
			got := nestedString(body, "assistant_message", "content")
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("%s answer=%q missing %q; body=%v", tc.id, got, want, body)
				}
			}
			for _, toolName := range tc.wantTools {
				if !containsToolCall(body["tool_calls"], toolName) {
					t.Fatalf("%s missing tool %s in body=%v", tc.id, toolName, body)
				}
			}
		})
	}
}

func TestAssistantChat_ConfigProposalScenarioCoverage(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	admin := env.LoginAdmin()
	deviceID := env.SeedDevice(testenv.DeviceFixture{Name: "config-ops-001", Hostname: "config-ops-001", Online: true, Roles: devicemodel.RoleBitServer})
	ruleKey := fmt.Sprintf("e2e_cpu_high_%d", time.Now().UnixNano())
	draftArgs := fmt.Sprintf(`{"domain":"alert_rule","action":"create","lookback_seconds":3600,"rule":{"rule_key":%q,"conditions":[{"metric":"cpu_usage_percent","operator":">","threshold":80}]}}`, ruleKey)
	rule := aiopstools.AlertRuleConfigInput{
		RuleKey: ruleKey,
		Conditions: []aiopstools.AlertRuleCondition{
			{Metric: "cpu_usage_percent", Operator: ">", Threshold: 80},
		},
	}
	payload, hash, err := aiopstools.AlertRuleConfigDraftPayload("create", rule)
	if err != nil {
		t.Fatalf("build alert draft payload: %v", err)
	}
	applyArgs := fmt.Sprintf(`{"domain":"alert_rule","action":"create","confirmed":true,"draft_hash":%q,"payload":%s}`, hash, string(payload))
	badApplyArgs := fmt.Sprintf(`{"domain":"alert_rule","action":"create","confirmed":true,"draft_hash":"sha256:bad","payload":%s}`, string(payload))

	status, setting, err := env.DoJSON("PUT", "/api/v1/system-settings/agent/write_enabled", map[string]any{
		"value":     "true",
		"sensitive": false,
	}, admin.AccessToken)
	if err != nil || status != http.StatusOK {
		t.Fatalf("enable write gate: status=%d body=%v err=%v", status, setting, err)
	}

	scenarios := []struct {
		id           string
		title        string
		prompt       string
		mentions     []map[string]string
		script       []testenv.LLMReply
		wantTools    []string
		wantContains []string
	}{
		{
			id:     "O36",
			title:  "create alert proposal",
			prompt: "创建 CPU 使用率超过 80% 持续 5 分钟的告警，先给我确认卡片",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o36-draft", Name: "draft_config_change", Arguments: draftArgs}}},
				{Content: "已生成告警提案，请确认或取消。"},
			},
			wantTools:    []string{"draft_config_change"},
			wantContains: []string{"告警提案"},
		},
		{
			id:     "O37",
			title:  "confirm alert proposal applies exact draft",
			prompt: "确认应用刚才的 CPU 告警提案",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o37-apply", Name: "apply_config_change", Arguments: applyArgs}}},
				{Content: "已按确认的 draft_hash 应用告警规则。"},
			},
			wantTools:    []string{"apply_config_change"},
			wantContains: []string{"已按确认"},
		},
		{
			id:     "O38",
			title:  "changed draft hash is rejected",
			prompt: "把刚才告警阈值改一下并直接应用",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o38-apply", Name: "apply_config_change", Arguments: badApplyArgs}}},
				{Content: "draft_hash 不匹配，不能应用；需要重新生成提案并确认。"},
			},
			wantTools:    []string{"apply_config_change"},
			wantContains: []string{"draft_hash 不匹配"},
		},
		{
			id:       "O39",
			title:    "restart service stops at proposal",
			prompt:   "重启这个设备上的 nginx",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", deviceID), "label": "config-ops-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o39-search", Name: "ToolSearch", Arguments: `{"query":"select:host_restart_service"}`}}},
				{Content: "重启服务是写操作，我会先给出提案并等待确认，不会直接执行。"},
			},
			wantTools:    []string{"ToolSearch"},
			wantContains: []string{"等待确认"},
		},
		{
			id:       "O40",
			title:    "reject restart leaves operation unapplied",
			prompt:   "不要重启 nginx，取消刚才的动作",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", deviceID), "label": "config-ops-001"}},
			script: []testenv.LLMReply{
				{Content: "已取消重启提案，没有执行任何写操作。"},
			},
			wantContains: []string{"没有执行"},
		},
	}
	for _, tc := range scenarios {
		t.Run(tc.id+" "+tc.title, func(t *testing.T) {
			env.FakeLLM().SetScript(tc.script...)
			sessionID := createChatSession(t, env, admin.AccessToken, "E2E "+tc.id)
			payload := map[string]any{"content": tc.prompt}
			if len(tc.mentions) > 0 {
				payload["mentions"] = tc.mentions
			}
			status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), payload, admin.AccessToken)
			if err != nil || status != http.StatusOK {
				t.Fatalf("%s post: status=%d body=%v err=%v", tc.id, status, body, err)
			}
			got := nestedString(body, "assistant_message", "content")
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("%s answer=%q missing %q; body=%v", tc.id, got, want, body)
				}
			}
			for _, toolName := range tc.wantTools {
				if !containsToolCall(body["tool_calls"], toolName) {
					t.Fatalf("%s missing tool %s in body=%v", tc.id, toolName, body)
				}
			}
		})
	}
}

func TestAssistantChat_RemainingScenarioCoverage(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	admin := env.LoginAdmin()
	deviceID := env.SeedDevice(testenv.DeviceFixture{Name: "capture-001", Hostname: "capture-001", Online: true, Roles: devicemodel.RoleBitServer})
	status, setting, err := env.DoJSON("PUT", "/api/v1/system-settings/agent/write_enabled", map[string]any{
		"value":     "true",
		"sensitive": false,
	}, admin.AccessToken)
	if err != nil || status != http.StatusOK {
		t.Fatalf("enable delegation gate: status=%d body=%v err=%v", status, setting, err)
	}

	t.Run("O05 conversation memory stays in the same root session", func(t *testing.T) {
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E O05")
		env.FakeLLM().SetScript(testenv.LLMReply{Content: "已记住你关注 capture-001。"})
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{"content": "这次先关注 capture-001"}, admin.AccessToken)
		if err != nil || status != http.StatusOK || !strings.Contains(nestedString(body, "assistant_message", "content"), "capture-001") {
			t.Fatalf("O05 first turn: status=%d body=%v err=%v", status, body, err)
		}
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{ID: "o05-devices", Name: "query_devices", Arguments: `{"name_contains":"capture-001","limit":5}`}}},
			testenv.LLMReply{Content: "继续沿用 capture-001 作为上下文设备。"},
		)
		status, body, err = env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{"content": "继续看刚才那台设备"}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("O05 second turn: status=%d body=%v err=%v", status, body, err)
		}
		if !containsToolCall(body["tool_calls"], "query_devices") || !strings.Contains(nestedString(body, "assistant_message", "content"), "capture-001") {
			t.Fatalf("O05 did not continue context through query_devices: body=%v", body)
		}
	})

	t.Run("O18 dynamic log backend reports unconfigured at execution", func(t *testing.T) {
		env.FakeLLM().SetScript(
			testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{ID: "o18-log", Name: "query_logql", Arguments: `{"query":"{job=\"nginx\"}","start":"now-5m","limit":10}`}}},
			testenv.LLMReply{Content: "当前日志后端未配置，不能编造日志内容。"},
		)
		sessionID := createChatSession(t, env, admin.AccessToken, "E2E O18")
		status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{"content": "查一下 nginx 最近的错误日志"}, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("post status=%d body=%v err=%v", status, body, err)
		}
		if !containsToolCall(body["tool_calls"], "query_logql") {
			t.Fatalf("missing dynamic log tool call: %v", body)
		}
		status, messages, err := env.DoJSON("GET", chatMessagesPath(sessionID), nil, admin.AccessToken)
		if err != nil || status != http.StatusOK {
			t.Fatalf("messages status=%d err=%v", status, err)
		}
		raw, err := json.Marshal(messages)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "Loki endpoint URL is empty") {
			t.Fatalf("missing explicit backend failure: %s", raw)
		}
	})

	for _, tc := range []struct {
		id            string
		prompt        string
		forbiddenTool string
		answer        string
	}{
		{id: "O19", prompt: "查一下 checkout 请求的 trace 慢在哪里", forbiddenTool: "query_traceql", answer: "当前 trace 后端未配置，不能编造链路数据。"},
	} {
		t.Run(tc.id+" unavailable signal capability is not disclosed", func(t *testing.T) {
			env.FakeLLM().SetScript(testenv.LLMReply{Content: tc.answer})
			sessionID := createChatSession(t, env, admin.AccessToken, "E2E "+tc.id)
			status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), map[string]any{"content": tc.prompt}, admin.AccessToken)
			if err != nil || status != http.StatusOK {
				t.Fatalf("%s post: status=%d body=%v err=%v", tc.id, status, body, err)
			}
			if got := nestedString(body, "assistant_message", "content"); !strings.Contains(got, "不能编造") {
				t.Fatalf("%s answer=%q body=%v", tc.id, got, body)
			}
			if containsToolCall(body["tool_calls"], tc.forbiddenTool) {
				t.Fatalf("%s should not execute unavailable tool %s: body=%v", tc.id, tc.forbiddenTool, body)
			}
			requests := env.FakeLLM().Requests()
			if len(requests) == 0 {
				t.Fatalf("%s no LLM request captured", tc.id)
			}
			if containsString(requests[0].ToolNames, tc.forbiddenTool) {
				t.Fatalf("%s exposed unavailable tool %s in %+v", tc.id, tc.forbiddenTool, requests[0].ToolNames)
			}
		})
	}

	scenarios := []struct {
		id           string
		title        string
		prompt       string
		mentions     []map[string]string
		script       []testenv.LLMReply
		wantTools    []string
		wantContains []string
	}{
		{
			id:     "O20",
			title:  "metric log consistency handles partial evidence",
			prompt: "把错误率指标和日志一起对一下",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o20-prom", Name: "query_promql", Arguments: `{"expr":"rate(http_requests_total{status=~\"5..\"}[5m])"}`}}},
				{Content: "已有指标证据，但日志能力未配置；结论必须标注不完整。"},
			},
			wantTools:    []string{"query_promql"},
			wantContains: []string{"不完整"},
		},
		{
			id:       "O27",
			title:    "tcp reachability requires explicit host and does not run arbitrary bash directly",
			prompt:   "在这台设备上测试 10.0.0.10:443 连通性",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", deviceID), "label": "capture-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o27-search", Name: "ToolSearch", Arguments: `{"query":"select:host_bash"}`}}},
				{Content: "连通性测试需要明确命令提案和权限；当前不会直接执行任意 shell。"},
			},
			wantTools:    []string{"ToolSearch"},
			wantContains: []string{"不会直接执行"},
		},
		{
			id:       "O33",
			title:    "multi capture request stays one named session",
			prompt:   "任务名 HTTPS 排障抓包，分两次在这台设备抓 60 秒 tcp port 443",
			mentions: []map[string]string{{"type": "device", "id": fmt.Sprintf("%d", deviceID), "label": "capture-001"}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o33-search", Name: "ToolSearch", Arguments: `{"query":"select:capture_pcap"}`}}},
				{Content: "这是一个抓包会话，包含两次 capture member，而不是两个独立会话。"},
			},
			wantTools:    []string{"ToolSearch"},
			wantContains: []string{"一个抓包会话"},
		},
		{
			id:     "O34",
			title:  "stop capture action is routed through cancelable task control",
			prompt: "停止刚才的抓包任务",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o34-stop", Name: "TaskStop", Arguments: `{"task_id":"capture-missing"}`}}},
				{Content: "没有找到可停止的运行任务；不会重复排队。"},
			},
			wantTools:    []string{"TaskStop"},
			wantContains: []string{"没有找到"},
		},
		{
			id:     "O35",
			title:  "artifact analysis requires existing artifact",
			prompt: "分析刚才完成的数据包产物",
			script: []testenv.LLMReply{
				{Content: "当前会话没有已完成的数据包产物，不能生成分析。"},
			},
			wantContains: []string{"没有已完成"},
		},
		{
			id:     "O43",
			title:  "kubernetes skill uses installed capability when present",
			prompt: "查看 Kubernetes 集群快照",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o43-k8s", Name: "query_k8s_snapshot", Arguments: `{}`}}},
				{Content: "Kubernetes 能力已被选择；没有连接时返回未配置而不是编造集群。"},
			},
			wantTools:    []string{"query_k8s_snapshot"},
			wantContains: []string{"不是编造"},
		},
		{
			id:     "O44",
			title:  "external mcp capability absence settles",
			prompt: "用 Grafana MCP 查 dashboard",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o44-search", Name: "ToolSearch", Arguments: `{"query":"select:grafana"}`}}},
				{Content: "当前没有可用的 Grafana MCP 连接，无法查询 dashboard。"},
			},
			wantTools:    []string{"ToolSearch"},
			wantContains: []string{"没有可用"},
		},
		{
			id:     "O46",
			title:  "disk specialist stays delegated and permission bounded",
			prompt: "让磁盘专家分析一下磁盘风险",
			mentions: []map[string]string{{
				"type": "device", "id": fmt.Sprintf("%d", deviceID), "label": "capture-001",
			}},
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o46-agent", Name: "AgentTool", Arguments: `{"description":"磁盘风险分析","subagent_type":"specialist-disk","prompt":"只做只读磁盘风险分析，返回证据和缺口。"}`}}},
				{Content: "磁盘专家：缺少目标设备，不能执行主机读取。"},
				{Content: "根对话收到磁盘专家结果：需要用户选择目标设备。"},
			},
			wantTools:    []string{"AgentTool"},
			wantContains: []string{"根对话收到磁盘专家结果"},
		},
		{
			id:     "O47",
			title:  "delegation depth cap returns to root",
			prompt: "让专家继续递归找更多专家排查",
			script: []testenv.LLMReply{
				{ToolCalls: []testenv.LLMToolCall{{ID: "o47-agent", Name: "AgentTool", Arguments: `{"description":"递归排查请求","subagent_type":"specialist-sre","prompt":"不要再委托子 Agent，只总结可验证证据。"}`}}},
				{Content: "专家按限制没有继续递归委托。"},
				{Content: "根对话收到结果：delegation 已停止在一层。"},
			},
			wantTools:    []string{"AgentTool"},
			wantContains: []string{"停止在一层"},
		},
	}
	for _, tc := range scenarios {
		t.Run(tc.id+" "+tc.title, func(t *testing.T) {
			env.FakeLLM().SetScript(tc.script...)
			sessionID := createChatSession(t, env, admin.AccessToken, "E2E "+tc.id)
			payload := map[string]any{"content": tc.prompt}
			if len(tc.mentions) > 0 {
				payload["mentions"] = tc.mentions
			}
			status, body, err := env.DoJSON("POST", chatMessagesPath(sessionID), payload, admin.AccessToken)
			if err != nil || status != http.StatusOK {
				t.Fatalf("%s post: status=%d body=%v err=%v", tc.id, status, body, err)
			}
			got := nestedString(body, "assistant_message", "content")
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("%s answer=%q missing %q; body=%v", tc.id, got, want, body)
				}
			}
			for _, toolName := range tc.wantTools {
				if !containsToolCall(body["tool_calls"], toolName) {
					t.Fatalf("%s missing tool %s in body=%v", tc.id, toolName, body)
				}
			}
		})
	}
}

func createChatSession(t *testing.T, env *testenv.Env, token, title string) string {
	t.Helper()
	return createVersionedChatSession(t, env, token, title, "v3")
}

func createVersionedChatSession(t *testing.T, env *testenv.Env, token, title, version string) string {
	t.Helper()
	status, body, err := env.DoJSON("POST", "/api/v1/chat/sessions", map[string]any{"title": title}, token)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create chat session: status=%d body=%v err=%v", status, body, err)
	}
	if body["runtime_version"] != version {
		t.Fatalf("new session runtime = %v, want %s", body["runtime_version"], version)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("create chat session returned empty id: %v", body)
	}
	t.Cleanup(func() {
		status, _, err := env.DoJSON("DELETE", "/api/v1/chat/sessions/"+id, nil, token)
		if err != nil || status != http.StatusNoContent {
			t.Errorf("cleanup chat session %s: status=%d err=%v", id, status, err)
		}
	})
	return id
}

func chatMessagesPath(sessionID string) string {
	return "/api/v1/chat/sessions/" + sessionID + "/messages"
}

func postChatStream(t *testing.T, env *testenv.Env, token, sessionID, content string) []string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatalf("marshal stream payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, env.BaseURL()+chatMessagesPath(sessionID)+"/stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("create stream request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status=%d body=%s", resp.StatusCode, raw)
	}

	var events []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	return events
}

func nestedString(body map[string]any, key, nested string) string {
	value, _ := body[key].(map[string]any)
	out, _ := value[nested].(string)
	return out
}

func containsToolCall(raw any, name string) bool {
	calls, _ := raw.([]any)
	for _, rawCall := range calls {
		call, _ := rawCall.(map[string]any)
		if call["name"] == name {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// O32 exercises the public approval path; merely mentioning a device must not
// authorize sensitive packet capture. Rejecting the proposal must unblock chat.
func TestAssistantChat_CaptureRequiresHumanApproval(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_TOOLBAG_DEFERRAL_THRESHOLD", "1"))
	admin := env.LoginAdmin()
	status, setting, err := env.DoJSON("PUT", "/api/v1/system-settings/agent/write_enabled", map[string]any{"value": "true", "sensitive": false}, admin.AccessToken)
	if err != nil || status != http.StatusOK {
		t.Fatalf("enable approval gate: status=%d body=%v err=%v", status, setting, err)
	}

	deviceID := env.SeedDevice(testenv.DeviceFixture{Name: "capture-approval", Hostname: "capture-approval", Online: true, Roles: devicemodel.RoleBitServer})
	env.FakeLLM().SetScript(
		testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{ID: "capture-search", Name: "ToolSearch", Arguments: `{"query":"select:capture_pcap"}`}}},
		testenv.LLMReply{ToolCalls: []testenv.LLMToolCall{{ID: "capture-proposal", Name: "capture_pcap", Arguments: fmt.Sprintf(`{"device_id":%d,"interface":"eth0","duration_seconds":60,"filter":"tcp port 443","session_name":"approval test"}`, deviceID)}}},
		testenv.LLMReply{Content: "抓包申请已拒绝，未执行抓包。"},
	)
	sessionID := createChatSession(t, env, admin.AccessToken, "E2E O32 approval")
	payload, err := json.Marshal(map[string]any{"content": "在这台设备上抓 60 秒 tcp port 443 的包，接口 eth0", "mentions": []map[string]string{{"type": "device", "id": fmt.Sprint(deviceID), "label": "capture-approval"}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env.BaseURL()+chatMessagesPath(sessionID)+"/stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+admin.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	var event string
	var rejected, done bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if event == "approval_pending" {
			if rejected {
				t.Fatal("duplicate approval")
			}
			var proposal struct {
				ApprovalID string `json:"approval_id"`
				ToolName   string `json:"tool_name"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &proposal); err != nil {
				t.Fatal(err)
			}
			if proposal.ApprovalID == "" || proposal.ToolName != "capture_pcap" {
				t.Fatalf("unexpected approval: %s", line)
			}
			status, row, err := env.DoJSON("GET", "/api/v1/approvals/"+proposal.ApprovalID, nil, admin.AccessToken)
			if err != nil || status != http.StatusOK || row["runtime_version"] != "v3" {
				t.Fatalf("approval origin lost: status=%d row=%v err=%v", status, row, err)
			}
			status, _, err = env.DoJSON("POST", "/api/v1/approvals/"+proposal.ApprovalID+"/reject", map[string]string{"reason": "E2E denies capture"}, admin.AccessToken)
			if err != nil || status != http.StatusOK {
				t.Fatalf("reject status=%d err=%v", status, err)
			}
			rejected = true
		}
		if event == "done" {
			done = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !rejected || !done {
		t.Log(env.ManagerLogs())
		t.Fatalf("rejected=%t, terminal=%t", rejected, done)
	}
}
