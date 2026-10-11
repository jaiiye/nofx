package kernel

import "nofx/mcp"

// The strategy layer is split into concerns so each can be reasoned about and
// tested on its own:
//
//	CandidateSelector  which symbols are in play this cycle
//	DecisionMaker      running the model and parsing its decision
//
// StrategyEngine implements CandidateSelector directly (its prompt-building
// methods, BuildSystemPrompt/BuildUserPrompt, are consumed inside the decision
// path rather than through an interface).

// CandidateSelector selects the candidate coin universe for a cycle.
type CandidateSelector interface {
	GetCandidateCoins() ([]CandidateCoin, error)
}

// DecisionMaker runs the LLM over a built context and parses its decision.
type DecisionMaker struct {
	engine *StrategyEngine
	client mcp.AIClient
}

// NewDecisionMaker builds a decision maker bound to an engine and model client.
func NewDecisionMaker(engine *StrategyEngine, client mcp.AIClient) *DecisionMaker {
	return &DecisionMaker{engine: engine, client: client}
}

// Decide performs one full decision cycle (prompt build, model call, parse).
func (d *DecisionMaker) Decide(ctx *Context, variant string) (*FullDecision, error) {
	return GetFullDecisionWithStrategy(ctx, d.client, d.engine, variant)
}
