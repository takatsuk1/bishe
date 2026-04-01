package orchestrator

import "ai/pkg/storage"

// buildMemoReminderWorkflowDef 构建备忘录提醒 agent 的编排流程
func buildMemoReminderWorkflowDef() storage.WorkflowDefinition {
	return storage.WorkflowDefinition{
		StartNodeID: "start",
		Nodes: []storage.NodeDef{
			{ID: "start", Type: "start", Metadata: map[string]string{"ui.label": "开始", "ui.x": "120", "ui.y": "120"}},
			{ID: "parse", Type: "chat_model", PreInput: "你是备忘录结构化助手，请提取用户输入中的提醒内容、提醒时间、弹窗内容，输出 json: {\"content\":...,\"remind_at\":...,\"script\":...}", Config: map[string]interface{}{"output_type": "json"}, Metadata: map[string]string{"ui.label": "解析提醒", "ui.x": "320", "ui.y": "120", "ui.agent": "memoreminder"}},
			{ID: "write_json", Type: "tool", Config: map[string]interface{}{"tool_name": "jsonfilemcp", "input_mapping": map[string]interface{}{"action": "write", "path": "reminders.json", "data": "parse.response"}}, Metadata: map[string]string{"ui.label": "写入 JSON", "ui.x": "520", "ui.y": "120", "ui.agent": "memoreminder"}},
			{ID: "end", Type: "end", Metadata: map[string]string{"ui.label": "结束", "ui.x": "760", "ui.y": "120"}},
		},
		Edges: []storage.EdgeDef{
			{From: "start", To: "parse"},
			{From: "parse", To: "write_json"},
			{From: "write_json", To: "end"},
		},
	}
}
