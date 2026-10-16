package chatruntime

import "strings"

// TurnPhase is the system-owned state of one AgentLoop iteration. LLM output
// may supply reasoning and tool arguments, but it may not skip a phase or
// upgrade a candidate target into an executable target.
type TurnPhase string

const (
	PhaseUnderstand TurnPhase = "understand"
	PhaseResolve    TurnPhase = "resolve"
	PhaseDecide     TurnPhase = "decide"
	PhaseClarify    TurnPhase = "clarify"
	PhasePropose    TurnPhase = "propose"
	PhaseAct        TurnPhase = "act"
	PhaseOperate    TurnPhase = "operate"
	PhaseObserve    TurnPhase = "observe"
	PhaseComplete   TurnPhase = "complete"
	PhaseReject     TurnPhase = "reject"
)

// Decision is the controlled branch selected after Resolve.
type Decision string

const (
	DecisionClarify Decision = "clarify"
	DecisionPropose Decision = "propose"
	DecisionAct     Decision = "act"
	DecisionOperate Decision = "operate"
	DecisionReject  Decision = "reject"
)

// ResolvedFacts are deterministic facts collected before an execution branch
// is exposed to the model.
type ResolvedFacts struct {
	Missing           bool
	AmbiguousTarget   bool
	Permitted         bool
	NeedsConfirmation bool
	LongRunning       bool
	Reason            string
}

// TurnPlan records the state transition and the boundary supplied to the LLM.
// It remains request-local: durable work is represented by an Operation, not
// an in-memory loop object.
type TurnPlan struct {
	Phase       TurnPhase
	Decision    Decision
	Facts       ResolvedFacts
	Transitions []TurnPhase
}

func PlanTurn(facts ResolvedFacts) TurnPlan {
	decision := Decide(facts)
	phase := PhaseAct
	switch decision {
	case DecisionClarify:
		phase = PhaseClarify
	case DecisionPropose:
		phase = PhasePropose
	case DecisionOperate:
		phase = PhaseOperate
	case DecisionReject:
		phase = PhaseReject
	}
	return TurnPlan{
		Phase:       phase,
		Decision:    decision,
		Facts:       facts,
		Transitions: []TurnPhase{PhaseUnderstand, PhaseResolve, PhaseDecide, phase},
	}
}

// Observe advances only an executable branch. A tool result is an observation
// that returns control to the loop; the caller either plans another turn or
// completes the current response. Long-running work completes later via its
// durable Operation, never by holding this request open.
func (p TurnPlan) Observe() TurnPhase {
	switch p.Phase {
	case PhaseAct, PhaseOperate:
		return PhaseObserve
	default:
		return p.Phase
	}
}

// NextAfterObserve is the only loop-back edge. A tool result is evidence,
// not a final decision, so the assistant re-enters Understand with the new
// observation before it may act again or complete the user-facing response.
func (p TurnPlan) NextAfterObserve() TurnPhase {
	if p.Observe() == PhaseObserve {
		return PhaseUnderstand
	}
	return p.Phase
}

func (p TurnPlan) ModelBoundary() string {
	switch p.Phase {
	case PhaseOperate:
		return "当前阶段是 Operate：只创建或更新可追踪、可取消的 Operation；不要等待长任务完成。"
	case PhaseAct:
		return "当前阶段是 Act：可执行短时、直接的工具调用；获得观察结果后继续推理。"
	case PhasePropose:
		return "当前阶段是 Propose：只展示可确认的提案，不执行变更。"
	default:
		return "当前阶段不允许工具执行。"
	}
}

func Decide(f ResolvedFacts) Decision {
	if strings.TrimSpace(f.Reason) != "" && !f.Permitted {
		return DecisionReject
	}
	if f.Missing || f.AmbiguousTarget {
		return DecisionClarify
	}
	if !f.Permitted {
		return DecisionReject
	}
	if f.NeedsConfirmation {
		return DecisionPropose
	}
	if f.LongRunning {
		return DecisionOperate
	}
	return DecisionAct
}
