package interviewsimulator

import (
	internalproto "ai/pkg/protocol"
	internaltm "ai/pkg/taskmanager"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

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

func extractJobDescription(query string) string {
	return extractBracketBlock(stripStateToken(query), "[job_description]")
}

func extractBracketBlock(text, tag string) string {
	idx := strings.LastIndex(strings.ToLower(text), strings.ToLower(tag))
	if idx < 0 {
		return ""
	}
	body := text[idx+len(tag):]
	next := len(body)
	for _, marker := range []string{"\n[upload]", "\n[content]", "\n[warning]", "\n[user]", "\n[assistant]"} {
		if i := strings.Index(strings.ToLower(body), marker); i >= 0 && i < next {
			next = i
		}
	}
	return strings.TrimSpace(body[:next])
}

func removeBracketBlock(text, tag string) string {
	lower := strings.ToLower(text)
	idx := strings.LastIndex(lower, strings.ToLower(tag))
	if idx < 0 {
		return text
	}
	body := text[idx+len(tag):]
	next := len(body)
	for _, marker := range []string{"\n[upload]", "\n[content]", "\n[warning]", "\n[user]", "\n[assistant]"} {
		if i := strings.Index(strings.ToLower(body), marker); i >= 0 && i < next {
			next = i
		}
	}
	return strings.TrimSpace(text[:idx] + body[next:])
}

func ensureProfileIncludesJD(st *InterviewState) string {
	profile := strings.TrimSpace(st.ProfileSummary)
	jd := strings.TrimSpace(st.JobDescription)
	if jd == "" {
		return profile
	}
	if strings.Contains(profile, jd) {
		return profile
	}
	return strings.TrimSpace("目标岗位JD：\n" + jd + "\n\n候选人画像：\n" + profile)
}

func buildPlanPrompt(st *InterviewState) string {
	return fmt.Sprintf("你是资深面试官。请严格基于目标岗位JD和候选人简历画像，生成%d道由浅入深的主问题。问题必须覆盖JD中的职责、必备技能、项目经验和风险点，并结合简历经历追问匹配度。只输出JSON数组，每项字段为question/focus/difficulty。\n\n目标岗位JD：\n%s\n\n候选人画像与简历摘要：\n%s", st.MaxRounds, strings.TrimSpace(st.JobDescription), strings.TrimSpace(st.ProfileSummary))
}

func buildScorePrompt(st *InterviewState, ans string) string {
	return "你是面试评分官。请根据目标岗位JD、原问题和候选人回答评分，只输出JSON对象(total/correctness/depth/expression/structure/risk/highlights/weaknesses)。\n\n目标岗位JD：\n" + strings.TrimSpace(st.JobDescription) + "\n\n问题：\n" + st.LastQuestion + "\n\n回答：\n" + ans
}

func buildFollowupPrompt(st *InterviewState, sc InterviewScore, strategy string) string {
	return "你是面试官。请结合目标岗位JD、候选人上一题回答评分和策略，生成1条贴近岗位要求的追问，只输出追问句。\n\n目标岗位JD：\n" + strings.TrimSpace(st.JobDescription) + "\n\n策略：" + strategy + "\n原问题：" + st.LastQuestion + "\n评分：" + scoreSummary(sc)
}

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
	raw = removeBracketBlock(raw, "[job_description]")
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
