package monitor

// AlertRules 警报规则
type AlertRules struct {
	NodeSlowThresholdMs     int64 // 节点慢阈值（毫秒）
	WorkflowSlowThresholdMs int64 // 工作流慢阈值（毫秒）
}

// DefaultAlertRules 获取默认警报规则
// 返回值:
//   默认警报规则
func DefaultAlertRules() AlertRules {
	return AlertRules{
		NodeSlowThresholdMs:     3000,     // 默认节点慢阈值：3秒
		WorkflowSlowThresholdMs: 10000,    // 默认工作流慢阈值：10秒
	}
}

// IsNodeSlow 判断节点是否慢
// 参数:
//   durationMs - 持续时间（毫秒）
// 返回值:
//   是否慢
func (r AlertRules) IsNodeSlow(durationMs int64) bool {
	threshold := r.NodeSlowThresholdMs
	if threshold <= 0 {
		threshold = 3000 // 默认阈值
	}
	return durationMs > threshold
}

// IsWorkflowSlow 判断工作流是否慢
// 参数:
//   durationMs - 持续时间（毫秒）
// 返回值:
//   是否慢
func (r AlertRules) IsWorkflowSlow(durationMs int64) bool {
	threshold := r.WorkflowSlowThresholdMs
	if threshold <= 0 {
		threshold = 10000 // 默认阈值
	}
	return durationMs > threshold
}
