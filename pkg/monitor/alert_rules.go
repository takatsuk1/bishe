package monitor

type AlertRules struct {
	NodeSlowThresholdMs     int64
	WorkflowSlowThresholdMs int64
}

func DefaultAlertRules() AlertRules {
	return AlertRules{
		NodeSlowThresholdMs:     3000,
		WorkflowSlowThresholdMs: 10000,
	}
}

func (r AlertRules) IsNodeSlow(durationMs int64) bool {
	threshold := r.NodeSlowThresholdMs
	if threshold <= 0 {
		threshold = 3000
	}
	return durationMs > threshold
}

func (r AlertRules) IsWorkflowSlow(durationMs int64) bool {
	threshold := r.WorkflowSlowThresholdMs
	if threshold <= 0 {
		threshold = 10000
	}
	return durationMs > threshold
}
