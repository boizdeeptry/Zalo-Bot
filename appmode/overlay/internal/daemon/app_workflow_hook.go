package daemon

import "agentdc/internal/ipc"

type appWorkflowDecision string

const (
	appWorkflowContinue appWorkflowDecision = "continue_agent"
	appWorkflowHandled  appWorkflowDecision = "handled"
	appWorkflowHandoff  appWorkflowDecision = "handoff"
)

func (a *api) evaluateAppWorkflow(ipc.ZaloEventRequest, ipc.ZaloMessage) (appWorkflowDecision, error) {
	return appWorkflowContinue, nil
}
