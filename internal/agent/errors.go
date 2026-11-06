package agent

import publicagent "github.com/m4ns0ur/iris/agent"

// Internal aliases keep existing packages and tests readable while the stable
// error values live in the public agent package.
var (
	ErrAgentBusy          = publicagent.ErrAgentBusy
	ErrAgentClosed        = publicagent.ErrAgentClosed
	ErrTurnBudgetExceeded = publicagent.ErrTurnBudgetExceeded
	ErrToolBudgetExceeded = publicagent.ErrToolBudgetExceeded
	ErrToolTimeout        = publicagent.ErrToolTimeout
)
