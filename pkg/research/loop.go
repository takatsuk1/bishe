package research

import (
	"ai/config"
	"ai/pkg/llm"
	internalproto "ai/pkg/protocol"
	"ai/pkg/tools"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type StepReporter interface {
	Report(ctx context.Context, ev internalproto.StepEvent)
}

type Evidence struct {
	Title   string
	URL     string
	Content string
	Score   float64
	Source  string
}

type RoundLog struct {
	Round      int
	Keywords   []string
	Results    int
	Selected   int
	SearchTime time.Duration
	Note       string
}

type Result struct {
	Answer   string
	Evidence []Evidence
	Rounds   []RoundLog
}

type Options struct {
	MaxRounds          int
	MaxQueriesPerRound int
	MaxResultsPerQuery int
	SearchDepth        string
	Language           string
}

type Runner struct {
	LLM    *llm.Client
	Tavily *tools.TavilyClient
	Models struct {
		Reasoning string
		Chat      string
	}
	Opt Options

	Reporter StepReporter
}

func (r *Runner) report(ctx context.Context, ev internalproto.StepEvent) {
	if r == nil || r.Reporter == nil {
		return
	}
	r.Reporter.Report(ctx, ev)
}

func NewRunner(tavily *tools.TavilyClient) *Runner {
	cfg := getMainConfigSafe()
	r := &Runner{}
	r.Tavily = tavily
	if cfg != nil {
		r.LLM = llm.NewClient(cfg.LLM.URL, cfg.LLM.APIKey)
		r.Models.Reasoning = cfg.LLM.ReasoningModel
		r.Models.Chat = cfg.LLM.ChatModel
	}
	r.Opt = Options{
		MaxRounds:          2,
		MaxQueriesPerRound: 3,
		MaxResultsPerQuery: 6,
		SearchDepth:        "basic",
		Language:           "zh",
	}
	return r
}

var splitKeywordRE = regexp.MustCompile(`[;；\n\r]+`)

func (r *Runner) Run(ctx context.Context, question string, seed []Evidence) (*Result, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, fmt.Errorf("question is empty")
	}

	evidence := make([]Evidence, 0, len(seed)+16)
	evidence = append(evidence, seed...)
	rounds := make([]RoundLog, 0, r.Opt.MaxRounds)

	for round := 1; round <= r.Opt.MaxRounds; round++ {
		evRound := internalproto.NewStepEvent("research", "round", "research.round.start", internalproto.StepStateStart, fmt.Sprintf("第%d轮：规划检索关键词...", round))
		evRound.Round = round
		r.report(ctx, evRound)

		keywords, stop, err := r.thinkKeywords(ctx, round, question, evidence)
		if err != nil {
			return nil, err
		}
		if stop || len(keywords) == 0 {
			evStop := internalproto.NewStepEvent("research", "round", "research.round.stop", internalproto.StepStateInfo, fmt.Sprintf("第%d轮：无需继续检索", round))
			evStop.Round = round
			r.report(ctx, evStop)
			rounds = append(rounds, RoundLog{Round: round, Keywords: keywords, Note: "无需继续检索"})
			break
		}
		if len(keywords) > r.Opt.MaxQueriesPerRound {
			keywords = keywords[:r.Opt.MaxQueriesPerRound]
		}

		start := time.Now()
		webResults, err := r.search(ctx, round, keywords)
		elapsed := time.Since(start)
		if err != nil {
			return nil, err
		}

		added := mergeWebEvidence(evidence, webResults)
		evidence = added
		evMerge := internalproto.NewStepEvent("research", "merge", "research.merge", internalproto.StepStateInfo, fmt.Sprintf("第%d轮：已合并检索结果（累计资料 %d 条）", round, len(evidence)))
		evMerge.Round = round
		r.report(ctx, evMerge)

		selected := 0
		for _, ev := range evidence {
			if ev.Source == "tavily" {
				selected++
			}
		}
		rounds = append(rounds, RoundLog{Round: round, Keywords: keywords, Results: len(webResults), Selected: selected, SearchTime: elapsed})

		evEnd := internalproto.NewStepEvent("research", "round", "research.round.end", internalproto.StepStateEnd, fmt.Sprintf("第%d轮：检索完成（新增 %d 条）", round, len(webResults)))
		evEnd.Round = round
		r.report(ctx, evEnd)
	}

	finalEvidence := cleanAndRank(evidence)
	r.report(ctx, internalproto.NewStepEvent("research", "rank", "research.rank", internalproto.StepStateInfo, fmt.Sprintf("资料去重与排序完成（保留 %d 条）", len(finalEvidence))))
	r.report(ctx, internalproto.NewStepEvent("research", "llm", "research.llm.summarize.start", internalproto.StepStateStart, fmt.Sprintf("调用大模型生成总结（引用 %d 条资料）...", len(finalEvidence))))
	answerBody, err := r.summarize(ctx, question, finalEvidence)
	if err != nil {
		return nil, err
	}
	r.report(ctx, internalproto.NewStepEvent("research", "llm", "research.llm.summarize.end", internalproto.StepStateEnd, "大模型总结完成"))

	out := answerBody + "\n\n" + renderSearchProcess(rounds, seed) + "\n\n" + renderSources(finalEvidence)
	return &Result{Answer: strings.TrimSpace(out), Evidence: finalEvidence, Rounds: rounds}, nil
}

func (r *Runner) thinkKeywords(ctx context.Context, round int, question string, evidence []Evidence) ([]string, bool, error) {
	if r.LLM == nil {
		ev := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.skip", internalproto.StepStateInfo, "未配置大模型，使用原问题作为检索关键词")
		ev.Round = round
		r.report(ctx, ev)
		return []string{question}, false, nil
	}

	model := strings.TrimSpace(r.Models.Reasoning)
	if model == "" {
		model = strings.TrimSpace(r.Models.Chat)
	}
	if model == "" {
		ev := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.skip", internalproto.StepStateInfo, "未配置模型名，使用原问题作为检索关键词")
		ev.Round = round
		r.report(ctx, ev)
		return []string{question}, false, nil
	}

	startEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.start", internalproto.StepStateStart, "调用大模型生成检索关键词...")
	startEv.Round = round
	startEv.Model = model
	r.report(ctx, startEv)

	refText := buildEvidenceForPrompt(evidence, 6)
	system := `你是一个严谨的信息检索规划助手。
任务：判断当前“已知资料”是否足够回答用户问题；如果不够，生成 1~3 个独立且具体的中文检索关键词。
约束：
- 若已知资料足够：只输出“无需检索”。
- 若需要检索：只输出关键词列表，用分号 ; 分隔；不要输出任何解释、不要编号、不要加引号。
- 关键词必须包含明确主语/对象（避免过于宽泛），并尽量覆盖用户问题的不同子问题。`
	user := fmt.Sprintf("当前日期：%s\n\n用户问题：%s\n\n已知资料：\n%s\n", time.Now().Format("2006-01-02"), question, refText)

	resp, err := r.LLM.ChatCompletion(ctx, model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, intPtr(256), floatPtr(0.2))
	if err != nil {
		errEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.error", internalproto.StepStateError, "大模型不可用，退化为使用原问题作为检索关键词")
		errEv.Round = round
		errEv.Model = model
		r.report(ctx, errEv)
		return []string{question}, false, nil
	}

	out := strings.TrimSpace(resp)
	out = strings.Trim(out, "\"'“”")
	if out == "" {
		return nil, false, nil
	}
	if strings.Contains(out, "无需检索") || strings.Contains(out, "不需要检索") {
		endEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.end", internalproto.StepStateEnd, "大模型判断：无需继续检索")
		endEv.Round = round
		endEv.Model = model
		r.report(ctx, endEv)
		return nil, true, nil
	}

	parts := splitKeywordRE.Split(out, -1)
	keywords := make([]string, 0, 3)
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(p, "\"'“”"))
		if p == "" {
			continue
		}
		keywords = append(keywords, p)
		if len(keywords) >= 3 {
			break
		}
	}

	endEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.end", internalproto.StepStateEnd, fmt.Sprintf("生成检索关键词 %d 个", len(keywords)))
	endEv.Round = round
	endEv.Model = model
	r.report(ctx, endEv)
	return keywords, false, nil
}

func (r *Runner) search(ctx context.Context, round int, keywords []string) ([]tools.Result, error) {
	if r.Tavily == nil {
		return nil, fmt.Errorf("tavily client is nil")
	}

	all := make([]tools.Result, 0, len(keywords)*r.Opt.MaxResultsPerQuery)
	for _, q := range keywords {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}

		startEv := internalproto.NewStepEvent("research", "tool", "tavily.search.start", internalproto.StepStateStart, fmt.Sprintf("调用 Tavily 检索：%s", q))
		startEv.Round = round
		startEv.Keyword = q
		r.report(ctx, startEv)

		started := time.Now()
		rsp, err := r.Tavily.Search(ctx, &tools.SearchRequest{Query: q, SearchDepth: r.Opt.SearchDepth, MaxResults: r.Opt.MaxResultsPerQuery})
		if err != nil {
			errEv := internalproto.NewStepEvent("research", "tool", "tavily.search.error", internalproto.StepStateError, fmt.Sprintf("Tavily 检索失败：%s", q))
			errEv.Round = round
			errEv.Keyword = q
			r.report(ctx, errEv)
			return nil, err
		}

		endEv := internalproto.NewStepEvent("research", "tool", "tavily.search.end", internalproto.StepStateEnd, fmt.Sprintf("Tavily 检索完成：%s（%d 条，耗时 %s）", q, len(rsp.Results), time.Since(started).Truncate(10*time.Millisecond)))
		endEv.Round = round
		endEv.Keyword = q
		r.report(ctx, endEv)
		all = append(all, rsp.Results...)
	}
	return all, nil
}

func (r *Runner) summarize(ctx context.Context, question string, evidence []Evidence) (string, error) {
	if r.LLM == nil {
		return fallbackSummary(question, evidence), nil
	}

	model := strings.TrimSpace(r.Models.Chat)
	if model == "" {
		model = strings.TrimSpace(r.Models.Reasoning)
	}
	if model == "" {
		return fallbackSummary(question, evidence), nil
	}

	system := `你是一个中文信息整合与写作助手，必须严格基于提供的 Sources 回答，不允许编造。
写作要求：
- 先给 TL;DR（2~4 行）。
- 正文按用户问题的子主题分节（用清晰的小标题）。
- 任何事实性陈述后面必须带引用编号，如 [1] 或 [1][3]，编号只能来自 Sources 列表。
- 若 Sources 不足以回答某部分，必须在“信息缺口与下一步”中明确指出缺口，并说明还需要检索什么。
- 不要输出 Sources 列表（我会在外部追加）。`
	user := fmt.Sprintf("当前日期：%s\n\n用户问题：%s\n\nSources（可引用）：\n%s\n", time.Now().Format("2006-01-02"), question, renderSourcesForPrompt(evidence))

	resp, err := r.LLM.ChatCompletion(ctx, model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, intPtr(900), floatPtr(0.3))
	if err != nil {
		return fallbackSummary(question, evidence), nil
	}
	return strings.TrimSpace(resp), nil
}

func cleanAndRank(all []Evidence) []Evidence {
	out := make([]Evidence, 0, len(all))
	seen := map[string]bool{}
	for _, e := range all {
		key := strings.TrimSpace(e.URL)
		if key == "" {
			key = strings.TrimSpace(e.Title + "|" + e.Source)
		}
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		e.Title = strings.TrimSpace(e.Title)
		e.URL = strings.TrimSpace(e.URL)
		e.Content = strings.TrimSpace(e.Content)
		out = append(out, e)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func mergeWebEvidence(base []Evidence, results []tools.Result) []Evidence {
	out := make([]Evidence, 0, len(base)+len(results))
	out = append(out, base...)
	for _, r := range results {
		ev := Evidence{Title: r.Title, URL: r.URL, Content: r.Content, Score: r.Score, Source: "tavily"}
		out = append(out, ev)
	}
	return out
}

func buildEvidenceForPrompt(evidence []Evidence, limit int) string {
	if len(evidence) == 0 {
		return "(空)"
	}
	if limit <= 0 {
		limit = 6
	}
	var b strings.Builder
	shown := 0
	for _, e := range evidence {
		if shown >= limit {
			break
		}
		snippet := shorten(e.Content, 280)
		title := e.Title
		if title == "" {
			title = "(无标题)"
		}
		if e.URL != "" {
			fmt.Fprintf(&b, "- %s | %s\n  %s\n", title, e.URL, snippet)
		} else {
			fmt.Fprintf(&b, "- %s\n  %s\n", title, snippet)
		}
		shown++
	}
	return strings.TrimSpace(b.String())
}

func renderSourcesForPrompt(evidence []Evidence) string {
	if len(evidence) == 0 {
		return "(无)"
	}
	var b strings.Builder
	for i, e := range evidence {
		idx := i + 1
		title := e.Title
		if title == "" {
			title = "(无标题)"
		}
		snippet := shorten(e.Content, 420)
		if e.URL != "" {
			fmt.Fprintf(&b, "[%d] %s\nURL: %s\n摘录: %s\n\n", idx, title, e.URL, snippet)
		} else {
			fmt.Fprintf(&b, "[%d] %s\n摘录: %s\n\n", idx, title, snippet)
		}
	}
	return strings.TrimSpace(b.String())
}

func renderSources(evidence []Evidence) string {
	var b strings.Builder
	b.WriteString("Sources:\n")
	if len(evidence) == 0 {
		b.WriteString("(无)\n")
		return strings.TrimSpace(b.String())
	}
	for i, e := range evidence {
		idx := i + 1
		title := e.Title
		if title == "" {
			title = "(无标题)"
		}
		if e.URL != "" {
			fmt.Fprintf(&b, "[%d] %s - %s\n", idx, title, e.URL)
		} else {
			fmt.Fprintf(&b, "[%d] %s\n", idx, title)
		}
	}
	return strings.TrimSpace(b.String())
}

func renderSearchProcess(rounds []RoundLog, seed []Evidence) string {
	var b strings.Builder
	b.WriteString("检索过程：\n")
	if len(seed) > 0 {
		b.WriteString("- 已知资料：已提供（将其作为 Sources 的一部分）\n")
	} else {
		b.WriteString("- 已知资料：无\n")
	}
	if len(rounds) == 0 {
		b.WriteString("- 未执行联网检索\n")
		return strings.TrimSpace(b.String())
	}
	for _, r := range rounds {
		if r.Round == 0 {
			fmt.Fprintf(&b, "- %s\n", r.Note)
			continue
		}
		kw := "(空)"
		if len(r.Keywords) > 0 {
			kw = strings.Join(r.Keywords, "；")
		}
		if r.Note != "" {
			fmt.Fprintf(&b, "- Round %d：%s（关键词：%s）\n", r.Round, r.Note, kw)
			continue
		}
		fmt.Fprintf(&b, "- Round %d：关键词：%s；返回：%d；耗时：%s\n", r.Round, kw, r.Results, r.SearchTime)
	}
	return strings.TrimSpace(b.String())
}

func shorten(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if maxRunes <= 0 || len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
}

func getMainConfigSafe() (cfg *config.MainConfig) {
	defer func() {
		if r := recover(); r != nil {
			cfg = nil
		}
	}()
	return config.GetMainConfig()
}

func fallbackSummary(question string, evidence []Evidence) string {
	question = strings.TrimSpace(question)
	var b strings.Builder
	b.WriteString("TL;DR\n")
	if len(evidence) == 0 {
		b.WriteString("- 当前没有可用 Sources，无法给出可靠结论。[1]\n")
	} else {
		fmt.Fprintf(&b, "- 基于当前检索到的 %d 条 Sources，已整理出要点与可回答范围。[1]\n", len(evidence))
	}

	b.WriteString("\n核心要点\n")
	if len(evidence) == 0 {
		b.WriteString("- 需要先获取资料（例如：官网/公告/权威资料/原文链接）。[1]\n")
	} else {
		max := 3
		if len(evidence) < max {
			max = len(evidence)
		}
		for i := 0; i < max; i++ {
			sn := shorten(evidence[i].Content, 140)
			if sn == "" {
				sn = "（该来源未提供可用正文摘录）"
			}
			fmt.Fprintf(&b, "- %s[%d]\n", sn, i+1)
		}
	}

	b.WriteString("\n信息缺口与下一步\n")
	if len(evidence) == 0 {
		b.WriteString("- 缺口：没有任何可引用来源。下一步：提供链接/指定范围，或启用联网检索以获取 Sources。[1]\n")
		return strings.TrimSpace(b.String())
	}
	b.WriteString("- 若需要更全面回答，请补充更具体的子问题（时间范围/版本/地区/对象），或允许更多轮次检索以补齐 Sources。[1]\n")
	_ = question
	return strings.TrimSpace(b.String())
}

func intPtr(v int) *int { return &v }

func floatPtr(v float64) *float64 { return &v }

func init() {
}
