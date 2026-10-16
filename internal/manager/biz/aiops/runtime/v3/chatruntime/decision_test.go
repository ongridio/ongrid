package chatruntime

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name  string
		facts ResolvedFacts
		want  Decision
	}{
		{name: "ambiguous target cannot execute", facts: ResolvedFacts{AmbiguousTarget: true, Permitted: true, LongRunning: true}, want: DecisionClarify},
		{name: "missing input clarifies", facts: ResolvedFacts{Missing: true, Permitted: true}, want: DecisionClarify},
		{name: "permission rejection wins", facts: ResolvedFacts{Permitted: false, Reason: "viewer"}, want: DecisionReject},
		{name: "confirmation precedes operation", facts: ResolvedFacts{Permitted: true, NeedsConfirmation: true, LongRunning: true}, want: DecisionPropose},
		{name: "long work creates operation", facts: ResolvedFacts{Permitted: true, LongRunning: true}, want: DecisionOperate},
		{name: "short work stays in loop", facts: ResolvedFacts{Permitted: true}, want: DecisionAct},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.facts); got != tt.want {
				t.Fatalf("Decide(%+v) = %q, want %q", tt.facts, got, tt.want)
			}
		})
	}
}

func TestConfirmedDeviceIDs(t *testing.T) {
	got := confirmedDeviceIDs([]Mention{{Type: "device", ID: "24"}, {Type: "alert", ID: "9"}, {Type: "device", ID: "bad"}, {Type: "DEVICE", ID: "25"}})
	if len(got) != 2 || got[0] != 24 || got[1] != 25 {
		t.Fatalf("confirmedDeviceIDs = %v", got)
	}
}

func TestResolveTurnProposesPacketCapture(t *testing.T) {
	plan, clarification := resolveTurn(&Request{UserText: "抓 60 秒 HTTPS 流量", Role: "admin"})
	if plan.Decision != DecisionPropose || plan.Phase != PhasePropose || clarification != "" {
		t.Fatalf("resolveTurn missing current-turn target = %+v, %q; want semantic resolution then proposal", plan, clarification)
	}
	plan, _ = resolveTurn(&Request{UserText: "抓包", Role: "admin", Mentions: []Mention{{Type: "device", ID: "24"}}})
	if plan.Decision != DecisionPropose || plan.Phase != PhasePropose || plan.Observe() != PhasePropose {
		t.Fatalf("resolveTurn selected target = %+v", plan)
	}
	plan, clarification = resolveTurn(&Request{UserText: "抓 60 秒 tcp port 443 的包", Role: "admin"})
	if plan.Decision != DecisionPropose || clarification != "" {
		t.Fatalf("resolveTurn packet wording = %+v, %q", plan, clarification)
	}
	plan, clarification = resolveTurn(&Request{UserText: "Capture packets on this device", Role: "admin"})
	if plan.Decision != DecisionPropose || clarification != "" {
		t.Fatalf("resolveTurn English capture without current target = %+v, %q", plan, clarification)
	}
	plan, clarification = resolveTurn(&Request{UserText: "Capture packets on device_id=24 interface eth0", Role: "admin"})
	if plan.Decision != DecisionPropose || clarification != "" {
		t.Fatalf("resolveTurn English capture with current target = %+v, %q", plan, clarification)
	}
	plan, clarification = resolveTurn(&Request{UserText: "停止刚才的抓包任务", Role: "admin"})
	if plan.Decision == DecisionClarify || clarification != "" {
		t.Fatalf("resolveTurn stop capture = %+v, %q; want agent loop", plan, clarification)
	}
}

func TestResolveTurnAllowsReadOnlyPacketAndGlobalRequests(t *testing.T) {
	tests := []string{
		"在源码里找 packet capture 会话列表 API 的入口文件，只给路径和函数名。",
		"分析最近 packet capture 页面为什么可能 page not found，从产物关联角度回答。",
		"跨 Edge 抓包能不能直接用时间差判断网络延迟？请基于当前产品能力回答。",
		"用 PromQL 算出各设备根分区磁盘使用率，按高到低排序。",
		"列出当前可用的磁盘、CPU、内存相关指标名。",
		"有没有 Linux 磁盘占用排查的内置知识？给我相关条目标题。",
		"在源码里搜索 ToolSearch 的实现位置，给出文件路径。",
	}
	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			plan, clarification := resolveTurn(&Request{UserText: text, Role: "admin"})
			if plan.Decision == DecisionClarify || clarification != "" {
				t.Fatalf("resolveTurn(%q) = %+v, %q; want agent loop", text, plan, clarification)
			}
		})
	}
}

func TestResolveTurnClarifiesMissingPacketCaptureSessionID(t *testing.T) {
	for _, text := range []string{
		"告诉我最近的数据包会话有哪些，给出会话名、pcap 数量和分析入口。",
		"分析最近一个 HTTPS 排障抓包会话，说明主要通信端点。",
	} {
		t.Run(text, func(t *testing.T) {
			plan, clarification := resolveTurn(&Request{UserText: text, Role: "admin"})
			if plan.Decision != DecisionClarify || !strings.Contains(clarification, "pcap-session") {
				t.Fatalf("resolveTurn(%q) = %+v, %q; want pcap-session clarification", text, plan, clarification)
			}
		})
	}
}

func TestResolveTurnClarifiesAmbiguousPacketPath(t *testing.T) {
	plan, clarification := resolveTurn(&Request{UserText: "这个包是不是经过 NAT 或网关了，帮我看一下链路。", Role: "admin"})
	if plan.Decision != DecisionClarify || !strings.Contains(clarification, "源/目的地址") {
		t.Fatalf("resolveTurn ambiguous packet path = %+v, %q; want packet details clarification", plan, clarification)
	}
}

func TestResolveTurnLetsLLMUnderstandReadOnlyHostRequests(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "disk", text: "磁盘快满了，帮我看看"},
		{name: "directory", text: "看看 /var 哪些目录最大"},
		{name: "process", text: "找一下占内存最高的进程"},
		{name: "interface", text: "检查网络接口错误包"},
		{name: "dns", text: "DNS 解析失败了，帮我排查"},
		{name: "english disk", text: "Which disk is using the most space on this device?"},
		{name: "english directory", text: "Show me the largest directories on this host"},
		{name: "english process", text: "Find the process using the most memory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, clarification := resolveTurn(&Request{UserText: tt.text, Role: "admin"})
			if plan.Decision == DecisionClarify || clarification != "" {
				t.Fatalf("resolveTurn(%q) = %+v, %q; want LLM understand path", tt.text, plan, clarification)
			}
		})
	}
}

func TestResolveTurnAllowsExplicitHostTarget(t *testing.T) {
	tests := []Request{
		{UserText: "看看 edge-001 的磁盘", Role: "admin"},
		{UserText: "检查 10.0.0.5 的网络接口", Role: "admin"},
		{UserText: "看看目录 /var", Role: "admin", Mentions: []Mention{{Type: "device", ID: "24"}}},
		{UserText: "device_id=24 查大文件", Role: "admin"},
	}
	for _, req := range tests {
		plan, clarification := resolveTurn(&req)
		if plan.Decision == DecisionClarify || clarification != "" {
			t.Fatalf("resolveTurn(%q) = %+v, %q; want non-clarify", req.UserText, plan, clarification)
		}
	}
}

func TestTurnPlanRecordsAndLoopsThroughSystemStates(t *testing.T) {
	plan := PlanTurn(ResolvedFacts{Permitted: true, LongRunning: true})
	want := []TurnPhase{PhaseUnderstand, PhaseResolve, PhaseDecide, PhaseOperate}
	if !reflect.DeepEqual(plan.Transitions, want) {
		t.Fatalf("Transitions = %v, want %v", plan.Transitions, want)
	}
	if plan.Observe() != PhaseObserve || plan.NextAfterObserve() != PhaseUnderstand {
		t.Fatalf("observe loop = %q -> %q, want observe -> understand", plan.Observe(), plan.NextAfterObserve())
	}

	clarify := PlanTurn(ResolvedFacts{Permitted: true, Missing: true})
	if clarify.NextAfterObserve() != PhaseClarify {
		t.Fatalf("non-executable plan looped to %q, want clarify", clarify.NextAfterObserve())
	}
}
