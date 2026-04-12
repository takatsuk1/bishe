package interviewsimulator

import (
	"ai/config"
	"ai/pkg/llm"
	"ai/pkg/logger"
	"ai/pkg/monitor"
	"ai/pkg/orchestrator"
	internalproto "ai/pkg/protocol"
	"ai/pkg/storage"
	internaltm "ai/pkg/taskmanager"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	InterviewSimulatorWorkflowID       = "interviewsimulator-default"
	InterviewSimulatorWorkflowWorkerID = "interviewsimulator_worker"
	InterviewSimulatorDefaultTaskType  = "interviewsimulator_default"
)

var stateTokenRe = regexp.MustCompile(`<!--INTERVIEW_STATE:([A-Za-z0-9+/=_-]+)-->`)

type ctxKeyTaskManager struct{}
type Agent struct {
	orchestratorEngine orchestrator.Engine
	llmClient          *llm.Client
	chatModel          string
}
type workflowNodeWorker struct{ agent *Agent }
type InterviewQuestion struct{ Question, Focus, Difficulty string }
type InterviewScore struct {
	Round, Total, Correctness, Depth, Expression, Structure, Risk int
	Question, Answer                                              string
	Highlights, Weaknesses                                        []string
}
type InterviewState struct {
	MaxRounds, NextQuestionIndex int
	LastQuestion, ProfileSummary string
	QuestionPlan                 []InterviewQuestion
	Scores                       []InterviewScore
}

var interviewNodeTypeText = map[string]string{
	"start": "start", "analyze": "chat_model", "plan": "chat_model",
	"score": "chat_model", "followup": "chat_model", "question": "chat_model", "end": "end",
}

func NewAgent() (*Agent, error) {
	cfg := config.GetMainConfig()
	a := &Agent{llmClient: llm.NewClient(cfg.LLM.URL, cfg.LLM.APIKey), chatModel: strings.TrimSpace(cfg.LLM.ChatModel)}
	if a.chatModel == "" {
		a.chatModel = strings.TrimSpace(cfg.LLM.ReasoningModel)
	}
	if a.chatModel == "" {
		a.chatModel = "qwen3.5-flash"
	}
	engineCfg := orchestrator.Config{DefaultTaskTimeoutSec: cfg.Orchestrator.DefaultTaskTimeoutSec, RetryMaxAttempts: cfg.Orchestrator.Retry.MaxAttempts, RetryBaseBackoffMs: cfg.Orchestrator.Retry.BaseBackoffMs, RetryMaxBackoffMs: cfg.Orchestrator.Retry.MaxBackoffMs}
	if engineCfg.DefaultTaskTimeoutSec <= 0 {
		engineCfg.DefaultTaskTimeoutSec = 600
	}
	if engineCfg.RetryMaxAttempts <= 0 {
		engineCfg.RetryMaxAttempts = 3
	}
	if engineCfg.RetryBaseBackoffMs <= 0 {
		engineCfg.RetryBaseBackoffMs = 200
	}
	if engineCfg.RetryMaxBackoffMs <= 0 {
		engineCfg.RetryMaxBackoffMs = 5000
	}
	if st, e := storage.GetMySQLStorage(); e == nil && st != nil {
		engineCfg.MonitorService = monitor.NewService(st, nil)
	}
	a.orchestratorEngine = orchestrator.NewEngine(engineCfg, orchestrator.NewInMemoryAgentRegistry())
	if e := a.orchestratorEngine.RegisterWorker(orchestrator.AgentDescriptor{ID: InterviewSimulatorWorkflowWorkerID, Name: "interviewsimulator workflow worker", Capabilities: []orchestrator.AgentCapability{"chat_model", "interviewsimulator"}}, &workflowNodeWorker{agent: a}); e != nil {
		return nil, e
	}
	wf, e := buildInterviewSimulatorWorkflow()
	if e != nil {
		return nil, e
	}
	if e = a.orchestratorEngine.RegisterWorkflow(wf); e != nil {
		return nil, e
	}
	return a, nil
}

func (a *Agent) ProcessInternal(ctx context.Context, taskID string, initialMsg internalproto.Message, manager internaltm.Manager) error {
	qp := make([]string, 0, len(initialMsg.Parts))
	for _, p := range initialMsg.Parts {
		if p.Type == internalproto.PartTypeText && strings.TrimSpace(p.Text) != "" {
			qp = append(qp, strings.TrimSpace(p.Text))
		}
	}
	if len(qp) == 0 || a.orchestratorEngine == nil {
		return fmt.Errorf("invalid input")
	}
	ctx = withTaskManager(ctx, manager)
	query := strings.Join(qp, "\n")
	userID := strings.TrimSpace(fmt.Sprint(initialMsg.Metadata["user_id"]))
	runID, e := a.orchestratorEngine.StartWorkflow(ctx, InterviewSimulatorWorkflowID, map[string]any{"task_id": taskID, "query": query, "text": query, "input": query, "user_id": userID})
	if e != nil {
		return e
	}
	stop := a.startProgressReporter(ctx, taskID, runID, manager)
	defer stop()
	runResult, e := a.orchestratorEngine.WaitRun(ctx, runID)
	if e != nil {
		return e
	}
	if runResult.State != orchestrator.RunStateSucceeded {
		return fmt.Errorf("interviewsimulator workflow failed: %s", runResult.ErrorMessage)
	}
	out, _ := runResult.FinalOutput["response"].(string)
	if strings.TrimSpace(out) == "" {
		out = "面试模拟未生成有效输出，请重试。"
	}
	if manager != nil {
		_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, &internalproto.Message{Role: internalproto.MessageRoleAgent, Parts: []internalproto.Part{internalproto.NewTextPart(out)}})
	}
	return nil
}

func (a *Agent) emitInterviewStepEvent(ctx context.Context, manager internaltm.Manager, taskID, nodeID string, state internalproto.StepState) {
	if manager == nil {
		return
	}
	nodeName := strings.TrimSpace(nodeID)
	if nodeName == "" {
		nodeName = "unknown"
	}
	nodeType := strings.TrimSpace(interviewNodeTypeText[nodeName])
	if nodeType == "" {
		nodeType = "unknown"
	}
	msg := fmt.Sprintf("节点名:%s 节点类型:%s", nodeName, nodeType)
	ev := internalproto.NewStepEvent("interviewsimulator", "workflow", nodeID, state, msg)
	txt := msg
	if t, e := internalproto.EncodeStepToken(ev); e == nil {
		txt = msg + "\n" + t
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{Role: internalproto.MessageRoleAgent, Parts: []internalproto.Part{internalproto.NewTextPart(txt)}})
}

func (w *workflowNodeWorker) Execute(ctx context.Context, req orchestrator.ExecutionRequest) (orchestrator.ExecutionResult, error) {
	taskID, _ := req.Payload["task_id"].(string)
	query := extractNodeQuery(req.Payload)
	if req.NodeType != orchestrator.NodeTypeChatModel {
		return orchestrator.ExecutionResult{Output: map[string]any{"response": query}}, nil
	}
	out, e := w.agent.callChatModel(ctx, taskID, strings.TrimSpace(req.NodeID), req.Payload, req.NodeConfig)
	if e != nil {
		return orchestrator.ExecutionResult{}, e
	}
	return orchestrator.ExecutionResult{Output: out}, nil
}

func (a *Agent) callChatModel(ctx context.Context, taskID string, nodeID string, payload map[string]any, nodeCfg map[string]any) (map[string]any, error) {
	intent := strings.TrimSpace(fmt.Sprint(nodeCfg["intent"]))
	query := strings.TrimSpace(extractNodeQuery(payload))
	if query == "" {
		return nil, fmt.Errorf("query empty")
	}
	st := loadState(payload, query)
	model := strings.TrimSpace(a.chatModel)
	base := strings.TrimSpace(a.llmClient.BaseURL)
	key := strings.TrimSpace(a.llmClient.APIKey)
	call := func(prompt string) string {
		a.emitSemanticStep(ctx, taskID, "interviewsimulator.llm.start", internalproto.StepStateInfo, "正在调用大模型："+nodeID)
		client := llm.NewClient(base, key)
		var streamBuf strings.Builder
		lastEmitAt := time.Time{}
		r, e := client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: prompt}}, nil, nil, func(delta string) error {
			if strings.TrimSpace(delta) == "" {
				return nil
			}
			streamBuf.WriteString(delta)
			if !lastEmitAt.IsZero() && time.Since(lastEmitAt) < 150*time.Millisecond {
				return nil
			}
			lastEmitAt = time.Now()
			a.emitSemanticStep(ctx, taskID, "interviewsimulator.llm.delta", internalproto.StepStateInfo, "正在调用大模型："+cut(streamBuf.String(), 140))
			return nil
		})
		if e != nil {
			logger.Warnf("[interviewsimulator] llm fail task=%s intent=%s err=%v", taskID, intent, e)
			return ""
		}
		a.emitSemanticStep(ctx, taskID, "interviewsimulator.llm.end", internalproto.StepStateEnd, "完成：大模型处理")
		return strings.TrimSpace(r)
	}
	switch intent {
	case "analyze_profile":
		if st.ProfileSummary == "" {
			st.ProfileSummary = nonEmpty(call("你是面试官助理，基于简历提炼候选人画像、风险点和高频追问点：\n"+stripStateToken(query)), "候选人画像暂不可用，请继续面试。")
		}
		return map[string]any{"response": st.ProfileSummary, "state": st}, nil
	case "plan_interview":
		if len(st.QuestionPlan) == 0 {
			raw := call(fmt.Sprintf("基于画像生成%d道由浅入深主问题，只输出JSON数组，每项字段question/focus/difficulty。画像：%s", st.MaxRounds, st.ProfileSummary))
			st.QuestionPlan = parsePlan(raw, st.MaxRounds)
			if len(st.QuestionPlan) == 0 {
				st.QuestionPlan = fallbackPlan(st.MaxRounds)
			}
		}
		return map[string]any{"response": "plan_ready", "state": st, "question_plan": st.QuestionPlan}, nil
	case "score_answer":
		ans := extractCurrentUserInput(query)
		if st.LastQuestion == "" || skipScore(ans) {
			return map[string]any{"response": "score_skipped", "state": st, "score_summary": ""}, nil
		}
		raw := call("仅输出JSON对象(total/correctness/depth/expression/structure/risk/highlights/weaknesses)。问题：" + st.LastQuestion + "\n回答：" + ans)
		sc := parseScore(raw, st, ans)
		if sc.Total == 0 {
			sc = fallbackScore(st, ans)
		}
		st.Scores = append(st.Scores, sc)
		return map[string]any{"response": "score_ready", "state": st, "score": sc, "score_summary": scoreSummary(sc)}, nil
	case "adaptive_followup":
		sc, ok := payloadScore(payload)
		if !ok {
			return map[string]any{"response": "followup_skipped", "state": st, "followup": ""}, nil
		}
		strategy := scoreStrategy(sc.Total)
		fu := nonEmpty(call("根据评分生成1条追问，只输出追问句。策略："+strategy+"。原问题："+st.LastQuestion+"。评分："+scoreSummary(sc)), fallbackFollowup(strategy))
		return map[string]any{"response": fu, "state": st, "followup": fu}, nil
	case "ask_next_question":
		if len(st.QuestionPlan) == 0 {
			st.QuestionPlan = fallbackPlan(st.MaxRounds)
		}
		if st.NextQuestionIndex >= min(st.MaxRounds, len(st.QuestionPlan)) {
			return map[string]any{"response": finalSummary(st), "state": st}, nil
		}
		scText := strings.TrimSpace(fmt.Sprint(getNodeField(payload, "score", "score_summary")))
		fu := strings.TrimSpace(fmt.Sprint(getNodeField(payload, "followup", "followup")))
		q := st.QuestionPlan[st.NextQuestionIndex]
		st.NextQuestionIndex++
		st.LastQuestion = q.Question
		var b strings.Builder
		if st.NextQuestionIndex == 1 {
			b.WriteString("面试开始：结构化多轮模拟（出题->评分->追问）。\n")
		} else {
			b.WriteString("上一题评分：\n" + nonEmpty(scText, "暂无") + "\n")
			if fu != "" {
				b.WriteString("\n自适应追问：\n" + fu + "\n")
			}
		}
		b.WriteString(fmt.Sprintf("\n第 %d/%d 题（%s）\n%s\n\n请直接作答。", st.NextQuestionIndex, min(st.MaxRounds, len(st.QuestionPlan)), nonEmpty(q.Difficulty, "综合"), q.Question))
		b.WriteString("\n" + encodeStateToken(st))
		return map[string]any{"response": b.String(), "state": st}, nil
	default:
		return map[string]any{"response": query, "state": st}, nil
	}
}

func withTaskManager(ctx context.Context, m internaltm.Manager) context.Context {
	if ctx == nil || m == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyTaskManager{}, m)
}
func taskManagerFromContext(ctx context.Context) internaltm.Manager {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(ctxKeyTaskManager{}).(internaltm.Manager)
	return m
}

func (a *Agent) emitSemanticStep(ctx context.Context, taskID string, name string, state internalproto.StepState, message string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	ev := internalproto.NewStepEvent("interviewsimulator", "semantic", strings.TrimSpace(name), state, strings.TrimSpace(message))
	token, err := internalproto.EncodeStepToken(ev)
	if err != nil {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(token)},
	})
}
func (a *Agent) startProgressReporter(ctx context.Context, taskID, runID string, manager internaltm.Manager) func() {
	if manager == nil || a.orchestratorEngine == nil {
		return func() {}
	}
	stopCh, doneCh := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(doneCh)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		seen, fin := map[string]bool{}, map[string]bool{}
		for {
			run, e := a.orchestratorEngine.GetRun(ctx, runID)
			if e == nil {
				if id := strings.TrimSpace(run.CurrentNodeID); id != "" && !seen[id] {
					seen[id] = true
					a.emitInterviewStepEvent(ctx, manager, taskID, id, internalproto.StepStateStart)
				}
				for _, nr := range run.NodeResults {
					id := strings.TrimSpace(nr.NodeID)
					if id == "" || fin[id] {
						continue
					}
					if ss, ok := interviewToTerminalStepState(nr.State); ok {
						fin[id] = true
						a.emitInterviewStepEvent(ctx, manager, taskID, id, ss)
					}
				}
				if run.State != orchestrator.RunStateRunning {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(stopCh); <-doneCh }
}
func interviewToTerminalStepState(state orchestrator.TaskState) (internalproto.StepState, bool) {
	switch state {
	case orchestrator.TaskStateSucceeded:
		return internalproto.StepStateEnd, true
	case orchestrator.TaskStateFailed, orchestrator.TaskStateCanceled:
		return internalproto.StepStateError, true
	default:
		return "", false
	}
}
func extractNodeQuery(payload map[string]any) string {
	for _, k := range []string{"text", "input", "query"} {
		v := strings.TrimSpace(fmt.Sprint(payload[k]))
		if v != "" && v != "<nil>" {
			return v
		}
	}
	return ""
}

func loadState(payload map[string]any, query string) *InterviewState {
	for _, n := range []string{"question", "followup", "score", "plan", "analyze"} {
		if m, ok := payload[n].(map[string]any); ok {
			if st := decodeState(m["state"]); st != nil {
				return normalize(st)
			}
		}
	}
	if m := stateTokenRe.FindAllStringSubmatch(query, -1); len(m) > 0 {
		if b, e := base64.StdEncoding.DecodeString(m[len(m)-1][1]); e == nil {
			var st InterviewState
			if json.Unmarshal(b, &st) == nil {
				return normalize(&st)
			}
		}
	}
	return normalize(&InterviewState{MaxRounds: 8})
}
func normalize(st *InterviewState) *InterviewState {
	if st.MaxRounds <= 0 {
		st.MaxRounds = 8
	}
	if st.QuestionPlan == nil {
		st.QuestionPlan = []InterviewQuestion{}
	}
	if st.Scores == nil {
		st.Scores = []InterviewScore{}
	}
	return st
}
func decodeState(v any) *InterviewState {
	b, _ := json.Marshal(v)
	var st InterviewState
	if json.Unmarshal(b, &st) == nil {
		return &st
	}
	return nil
}
func encodeStateToken(st *InterviewState) string {
	b, e := json.Marshal(st)
	if e != nil {
		return ""
	}
	return "<!--INTERVIEW_STATE:" + base64.StdEncoding.EncodeToString(b) + "-->"
}
func stripStateToken(s string) string { return strings.TrimSpace(stateTokenRe.ReplaceAllString(s, "")) }
func parsePlan(raw string, rounds int) []InterviewQuestion {
	js := extractJSON(raw, '[', ']')
	if js == "" {
		return nil
	}
	var arr []InterviewQuestion
	if json.Unmarshal([]byte(js), &arr) != nil {
		return nil
	}
	out := make([]InterviewQuestion, 0, len(arr))
	for _, q := range arr {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		out = append(out, InterviewQuestion{Question: strings.TrimSpace(q.Question), Focus: strings.TrimSpace(q.Focus), Difficulty: nonEmpty(strings.TrimSpace(q.Difficulty), "intermediate")})
		if len(out) >= rounds {
			break
		}
	}
	return out
}
func fallbackPlan(rounds int) []InterviewQuestion {
	base := []InterviewQuestion{{"请做2分钟自我介绍并突出与岗位匹配经历。", "沟通表达", "basic"}, {"最近项目你的职责和关键决策是什么？", "项目复盘", "basic"}, {"最熟悉的后端能力如何落地？", "技术深度", "intermediate"}, {"高并发瓶颈如何排查优化？", "问题解决", "intermediate"}, {"跨团队协作困难如何推进？", "协作能力", "intermediate"}, {"你做过系统最该改进的架构点是什么？", "架构思维", "advanced"}, {"从0到1重做该系统你的优先级？", "系统设计", "advanced"}, {"你当前最大短板和改进计划？", "反思成长", "advanced"}}
	if rounds <= 0 || rounds > len(base) {
		rounds = len(base)
	}
	return base[:rounds]
}
func parseScore(raw string, st *InterviewState, ans string) InterviewScore {
	js := extractJSON(raw, '{', '}')
	if js == "" {
		return InterviewScore{}
	}
	var x struct {
		Total, Correctness, Depth, Expression, Structure, Risk int
		Highlights, Weaknesses                                 []string
	}
	if json.Unmarshal([]byte(js), &x) != nil {
		return InterviewScore{}
	}
	return InterviewScore{Round: len(st.Scores) + 1, Question: st.LastQuestion, Answer: cut(ans, 260), Total: clamp(x.Total), Correctness: clamp(x.Correctness), Depth: clamp(x.Depth), Expression: clamp(x.Expression), Structure: clamp(x.Structure), Risk: clamp(x.Risk), Highlights: x.Highlights, Weaknesses: x.Weaknesses}
}
func fallbackScore(st *InterviewState, ans string) InterviewScore {
	l := len([]rune(strings.TrimSpace(ans)))
	t := 55
	if l > 120 {
		t = 68
	}
	if l > 260 {
		t = 75
	}
	return InterviewScore{Round: len(st.Scores) + 1, Question: st.LastQuestion, Answer: cut(ans, 260), Total: t, Correctness: t - 5, Depth: t - 8, Expression: t + 2, Structure: t - 4, Risk: t - 6, Highlights: []string{"表达完整"}, Weaknesses: []string{"细节不足"}}
}
func scoreSummary(s InterviewScore) string {
	return fmt.Sprintf("总分 %d/100（正确性 %d，深度 %d，表达 %d，结构 %d，风险 %d）", s.Total, s.Correctness, s.Depth, s.Expression, s.Structure, s.Risk)
}
func scoreStrategy(total int) string {
	if total < 60 {
		return "basic_probe"
	}
	if total < 80 {
		return "scenario_probe"
	}
	return "architecture_probe"
}
func fallbackFollowup(s string) string {
	if s == "basic_probe" {
		return "请补充刚才方案的核心数据结构和关键接口。"
	}
	if s == "architecture_probe" {
		return "如果流量提升10倍你优先改造哪一层？为什么？"
	}
	return "如果线上出现同类问题，你如何在性能和稳定性间取舍？"
}
func finalSummary(st *InterviewState) string {
	if len(st.Scores) == 0 {
		return "面试结束：未获得可评分回答，请补充后继续。"
	}
	sum := 0
	for _, s := range st.Scores {
		sum += s.Total
	}
	avg := sum / len(st.Scores)
	return fmt.Sprintf("面试模拟完成：共%d轮，平均分%d/100。建议针对最低分维度做专项训练。", len(st.Scores), avg)
}
func payloadScore(payload map[string]any) (InterviewScore, bool) {
	if s, ok := getNodeField(payload, "score", "score").(map[string]any); ok {
		b, _ := json.Marshal(s)
		var o InterviewScore
		if json.Unmarshal(b, &o) == nil {
			return o, o.Total > 0
		}
	}
	return InterviewScore{}, false
}
func getNodeField(payload map[string]any, node, field string) any {
	if n, ok := payload[node].(map[string]any); ok {
		return n[field]
	}
	return nil
}
func extractCurrentUserInput(query string) string {
	raw := stripStateToken(query)
	if i := strings.LastIndex(raw, "=== 当前问题 ==="); i >= 0 {
		raw = raw[i+len("=== 当前问题 ==="):]
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		l := strings.ToLower(t)
		if strings.HasPrefix(l, "[upload]") || strings.HasPrefix(l, "[content]") || strings.HasPrefix(l, "[warning]") || strings.Contains(l, "(application/") || strings.Contains(l, ".pdf") || strings.Contains(l, ".docx") || strings.Contains(l, ".xlsx") {
			continue
		}
		out = append(out, t)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
func skipScore(ans string) bool {
	a := strings.ToLower(strings.TrimSpace(ans))
	if a == "" {
		return true
	}
	for _, k := range []string{"开始面试", "开始", "继续", "下一题", "next"} {
		if a == k {
			return true
		}
	}
	return false
}
func extractJSON(s string, open, close byte) string {
	i := strings.IndexByte(strings.TrimSpace(s), open)
	if i < 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	d := 0
	for j := i; j < len(s); j++ {
		if s[j] == open {
			d++
		} else if s[j] == close {
			d--
			if d == 0 {
				return s[i : j+1]
			}
		}
	}
	return ""
}
func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
func cut(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

func buildInterviewSimulatorWorkflow() (*orchestrator.Workflow, error) {
	wf, err := orchestrator.NewWorkflow(InterviewSimulatorWorkflowID, "interviewsimulator default workflow")
	if err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "start", Type: orchestrator.NodeTypeStart}); err != nil {
		return nil, err
	}
	add := func(id, intent, pre string) error {
		return wf.AddNode(orchestrator.Node{ID: id, Type: orchestrator.NodeTypeChatModel, AgentID: InterviewSimulatorWorkflowWorkerID, TaskType: InterviewSimulatorDefaultTaskType, Config: map[string]any{"intent": intent}, PreInput: pre})
	}
	if err = add("analyze", "analyze_profile", "分析用户简历内容，提炼候选人画像与面试关注点。"); err != nil {
		return nil, err
	}
	if err = add("plan", "plan_interview", "规划多轮面试主问题，要求由浅入深。"); err != nil {
		return nil, err
	}
	if err = add("score", "score_answer", "对当前用户回答进行多维度评分，输出结构化分数。"); err != nil {
		return nil, err
	}
	if err = add("followup", "adaptive_followup", "基于评分结果生成自适应追问。"); err != nil {
		return nil, err
	}
	if err = add("question", "ask_next_question", "输出下一轮主问题，并附带本轮评分与追问结果。"); err != nil {
		return nil, err
	}
	if err = wf.AddNode(orchestrator.Node{ID: "end", Type: orchestrator.NodeTypeEnd}); err != nil {
		return nil, err
	}
	for _, e := range [][2]string{{"start", "analyze"}, {"analyze", "plan"}, {"plan", "score"}, {"score", "followup"}, {"followup", "question"}, {"question", "end"}} {
		if err = wf.AddEdge(e[0], e[1]); err != nil {
			return nil, err
		}
	}
	return wf, nil
}
