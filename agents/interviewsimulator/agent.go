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
	streamedFinal := interviewStreamedToUser(runResult)
	if manager != nil {
		var doneMsg *internalproto.Message
		if !streamedFinal {
			doneMsg = &internalproto.Message{Role: internalproto.MessageRoleAgent, Parts: []internalproto.Part{internalproto.NewTextPart(out)}}
		}
		_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateCompleted, doneMsg)
	}
	return nil
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

func (a *Agent) emitAssistantDelta(ctx context.Context, taskID string, text string) {
	manager := taskManagerFromContext(ctx)
	if manager == nil {
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	_ = manager.UpdateTaskState(ctx, taskID, internalproto.TaskStateWorking, &internalproto.Message{
		Role:  internalproto.MessageRoleAgent,
		Parts: []internalproto.Part{internalproto.NewTextPart(text)},
	})
}

func interviewStreamedToUser(runResult orchestrator.RunResult) bool {
	for _, nr := range runResult.NodeResults {
		if nr.Output == nil {
			continue
		}
		v, ok := nr.Output["streamed_to_user"]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case bool:
			if t {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(t), "true") {
				return true
			}
		}
	}
	return false
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
	call := func(prompt string, streamToUser bool) (string, bool) {
		a.emitSemanticStep(ctx, taskID, "interviewsimulator.llm.start", internalproto.StepStateInfo, "正在调用大模型："+nodeID)
		client := llm.NewClient(base, key)
		var streamBuf strings.Builder
		var pending strings.Builder
		lastEmitAt := time.Time{}
		streamedToUser := false
		flushToUser := func(force bool) {
			if !streamToUser || pending.Len() == 0 {
				return
			}
			if !force && pending.Len() < 48 {
				return
			}
			a.emitAssistantDelta(ctx, taskID, pending.String())
			pending.Reset()
			streamedToUser = true
		}
		r, e := client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: prompt}}, nil, nil, func(delta string) error {
			if strings.TrimSpace(delta) == "" {
				return nil
			}
			streamBuf.WriteString(delta)
			if streamToUser {
				pending.WriteString(delta)
				flushToUser(false)
			}
			if !lastEmitAt.IsZero() && time.Since(lastEmitAt) < 150*time.Millisecond {
				return nil
			}
			lastEmitAt = time.Now()
			a.emitSemanticStep(ctx, taskID, "interviewsimulator.llm.delta", internalproto.StepStateInfo, "正在调用大模型："+cut(streamBuf.String(), 140))
			return nil
		})
		if e == nil {
			flushToUser(true)
		}
		if e != nil {
			logger.Warnf("[interviewsimulator] llm fail task=%s intent=%s err=%v", taskID, intent, e)
			return "", streamedToUser
		}
		a.emitSemanticStep(ctx, taskID, "interviewsimulator.llm.end", internalproto.StepStateEnd, "完成：大模型处理")
		return strings.TrimSpace(r), streamedToUser
	}
	switch intent {
	case "analyze_profile":
		if st.ProfileSummary == "" {
			raw, _ := call("你是面试官助理，基于简历提炼候选人画像、风险点和高频追问点：\n"+stripStateToken(query), false)
			st.ProfileSummary = nonEmpty(raw, "候选人画像暂不可用，请继续面试。")
		}
		return map[string]any{"response": st.ProfileSummary, "state": st}, nil
	case "plan_interview":
		if len(st.QuestionPlan) == 0 {
			raw, _ := call(fmt.Sprintf("基于画像生成%d道由浅入深主问题，只输出JSON数组，每项字段question/focus/difficulty。画像：%s", st.MaxRounds, st.ProfileSummary), false)
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
		raw, _ := call("仅输出JSON对象(total/correctness/depth/expression/structure/risk/highlights/weaknesses)。问题："+st.LastQuestion+"\n回答："+ans, false)
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
		fu, _ := call("根据评分生成1条追问，只输出追问句。策略："+strategy+"。原问题："+st.LastQuestion+"。评分："+scoreSummary(sc), false)
		fu = nonEmpty(fu, fallbackFollowup(strategy))
		return map[string]any{"response": fu, "state": st, "followup": fu}, nil
	case "ask_next_question":
		if len(st.QuestionPlan) == 0 {
			st.QuestionPlan = fallbackPlan(st.MaxRounds)
		}
		if st.NextQuestionIndex >= min(st.MaxRounds, len(st.QuestionPlan)) {
			summary, streamedToUser := call(finalSummary(st), true)
			return map[string]any{"response": summary, "state": st, "streamed_to_user": streamedToUser}, nil
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
