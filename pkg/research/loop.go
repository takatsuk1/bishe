package research

import (
	"ai/config"
	"ai/pkg/llm"
	"ai/pkg/logger"
	internalproto "ai/pkg/protocol"
	"ai/pkg/tools"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// StepReporter 步骤报告器接口
type StepReporter interface {
	// Report 报告步骤事件
	Report(ctx context.Context, ev internalproto.StepEvent)
}

// Evidence 表示证据
type Evidence struct {
	// Title 标题
	Title string
	// URL 链接
	URL string
	// Content 内容
	Content string
	// Score 分数
	Score float64
	// Source 来源（例如 "tavily", "jina", "elastic", "user"）
	Source string
}

// RoundLog 表示轮次日志
type RoundLog struct {
	// Round 轮次
	Round int
	// Keywords 关键词
	Keywords []string
	// Results 结果数量
	Results int
	// Selected 选择数量
	Selected int
	// SearchTime 搜索时间
	SearchTime time.Duration
	// Note 备注
	Note string
}

// Result 表示结果
type Result struct {
	// Answer 回答
	Answer string
	// Evidence 证据
	Evidence []Evidence
	// Rounds 轮次日志
	Rounds []RoundLog
}

// Options 表示选项
type Options struct {
	// MaxRounds 最大轮次
	MaxRounds int
	// MaxQueriesPerRound 每轮最大查询数
	MaxQueriesPerRound int
	// MaxResultsPerQuery 每个查询最大结果数
	MaxResultsPerQuery int
	// SearchDepth 搜索深度
	SearchDepth string
	// Language 语言
	Language string
}

// Runner 表示运行器
type Runner struct {
	// LLM 大模型客户端
	LLM *llm.Client
	// Tavily Tavily客户端
	Tavily *tools.TavilyClient
	// Models 模型配置
	Models struct {
		// Reasoning 推理模型
		Reasoning string
		// Chat 聊天模型
		Chat string
	}
	// Opt 选项
	Opt Options

	// Reporter 步骤报告器
	Reporter StepReporter
}

// report 报告步骤事件
// ctx: 上下文
// ev: 步骤事件
func (r *Runner) report(ctx context.Context, ev internalproto.StepEvent) {
	if r == nil || r.Reporter == nil {
		return
	}
	r.Reporter.Report(ctx, ev)
}

// NewRunner 创建一个新的运行器
// tavily: Tavily客户端
// 返回创建的运行器
func NewRunner(tavily *tools.TavilyClient) *Runner {
	// 安全获取主配置
	cfg := getMainConfigSafe()
	r := &Runner{}
	r.Tavily = tavily
	if cfg != nil {
		// 初始化大模型客户端
		r.LLM = llm.NewClient(cfg.LLM.URL, cfg.LLM.APIKey)
		r.Models.Reasoning = cfg.LLM.ReasoningModel
		r.Models.Chat = cfg.LLM.ChatModel
	}
	// 设置默认选项
	r.Opt = Options{
		MaxRounds:          2,
		MaxQueriesPerRound: 3,
		MaxResultsPerQuery: 6,
		SearchDepth:        "basic",
		Language:           "zh",
	}
	return r
}

// splitKeywordRE 用于分割关键词的正则表达式
var splitKeywordRE = regexp.MustCompile(`[;；\n\r]+`)

// Run 执行研究循环
// ctx: 上下文
// question: 问题
// seed: 种子证据
// 返回结果和可能的错误
func (r *Runner) Run(ctx context.Context, question string, seed []Evidence) (*Result, error) {
	// 清理问题
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, fmt.Errorf("question is empty")
	}

	// 初始化证据和轮次日志
	evidence := make([]Evidence, 0, len(seed)+16)
	evidence = append(evidence, seed...)
	rounds := make([]RoundLog, 0, r.Opt.MaxRounds)

	// 执行多轮检索
	for round := 1; round <= r.Opt.MaxRounds; round++ {
		// 报告轮次开始
		evRound := internalproto.NewStepEvent("research", "round", "research.round.start", internalproto.StepStateStart, fmt.Sprintf("第%d轮：规划检索关键词…", round))
		evRound.Round = round
		r.report(ctx, evRound)

		// 思考关键词
		keywords, stop, err := r.thinkKeywords(ctx, round, question, evidence)
		if err != nil {
			return nil, err
		}
		// 如果需要停止或没有关键词，则结束循环
		if stop || len(keywords) == 0 {
			evStop := internalproto.NewStepEvent("research", "round", "research.round.stop", internalproto.StepStateInfo, fmt.Sprintf("第%d轮：无需继续检索", round))
			evStop.Round = round
			r.report(ctx, evStop)
			rounds = append(rounds, RoundLog{Round: round, Keywords: keywords, Note: "无需继续检索"})
			break
		}
		// 限制每轮的查询数
		if len(keywords) > r.Opt.MaxQueriesPerRound {
			keywords = keywords[:r.Opt.MaxQueriesPerRound]
		}

		// 执行搜索
		start := time.Now()
		webResults, err := r.search(ctx, round, keywords)
		elapsed := time.Since(start)
		if err != nil {
			return nil, err
		}

		// 合并搜索结果
		added := mergeWebEvidence(evidence, webResults)
		evidence = added
		evMerge := internalproto.NewStepEvent("research", "merge", "research.merge", internalproto.StepStateInfo, fmt.Sprintf("第%d轮：已合并检索结果（累计资料 %d 条）", round, len(evidence)))
		evMerge.Round = round
		r.report(ctx, evMerge)

		// 统计选中的证据数量
		selected := 0
		for _, ev := range evidence {
			if ev.Source == "tavily" {
				selected++
			}
		}
		// 添加轮次日志
		rounds = append(rounds, RoundLog{Round: round, Keywords: keywords, Results: len(webResults), Selected: selected, SearchTime: elapsed})
		// 报告轮次结束
		evEnd := internalproto.NewStepEvent("research", "round", "research.round.end", internalproto.StepStateEnd, fmt.Sprintf("第%d轮：检索完成（新增 %d 条）", round, len(webResults)))
		evEnd.Round = round
		r.report(ctx, evEnd)
	}

	// 清理和排序证据
	finalEvidence := cleanAndRank(evidence)
	r.report(ctx, internalproto.NewStepEvent("research", "rank", "research.rank", internalproto.StepStateInfo, fmt.Sprintf("资料去重与排序完成（保留 %d 条）", len(finalEvidence))))
	// 生成总结
	r.report(ctx, internalproto.NewStepEvent("research", "llm", "research.llm.summarize.start", internalproto.StepStateStart, fmt.Sprintf("调用大模型生成总结（引用 %d 条资料）…", len(finalEvidence))))
	answerBody, err := r.summarize(ctx, question, finalEvidence)
	if err != nil {
		return nil, err
	}
	r.report(ctx, internalproto.NewStepEvent("research", "llm", "research.llm.summarize.end", internalproto.StepStateEnd, "大模型总结完成"))

	// 构建输出
	out := answerBody + "\n\n" + renderSearchProcess(rounds, seed) + "\n\n" + renderSources(finalEvidence)
	return &Result{Answer: strings.TrimSpace(out), Evidence: finalEvidence, Rounds: rounds}, nil
}

// thinkKeywords 思考关键词
// ctx: 上下文
// round: 轮次
// question: 问题
// evidence: 证据
// 返回关键词、是否停止和可能的错误
func (r *Runner) thinkKeywords(ctx context.Context, round int, question string, evidence []Evidence) ([]string, bool, error) {
	// 如果没有配置大模型，使用原问题作为关键词
	if r.LLM == nil {
		ev := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.skip", internalproto.StepStateInfo, "未配置大模型，使用原问题作为检索关键词")
		ev.Round = round
		r.report(ctx, ev)
		return []string{question}, false, nil
	}
	// 获取模型名称
	model := strings.TrimSpace(r.Models.Reasoning)
	if model == "" {
		model = strings.TrimSpace(r.Models.Chat)
	}
	// 如果没有配置模型，使用原问题作为关键词
	if model == "" {
		ev := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.skip", internalproto.StepStateInfo, "未配置模型名，使用原问题作为检索关键词")
		ev.Round = round
		r.report(ctx, ev)
		return []string{question}, false, nil
	}
	// 报告开始思考关键词
	startEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.start", internalproto.StepStateStart, "调用大模型生成检索关键词…")
	startEv.Round = round
	startEv.Model = model
	r.report(ctx, startEv)

	// 构建证据提示
	refText := buildEvidenceForPrompt(evidence, 6)
	// 系统提示
	system := `你是一个严谨的信息检索规划助手。\n` +
		`任务：判断当前“已知资料”是否足够回答用户问题；如果不够，生成 1~3 个独立且具体的中文检索关键词。\n` +
		`约束：\n` +
		`- 若已知资料足够：只输出“无需检索”。\n` +
		`- 若需要检索：只输出关键词列表，用分号 ; 分隔；不要输出任何解释、不要编号、不要加引号。\n` +
		`- 关键词必须包含明确主语/对象（避免过于宽泛），并尽量覆盖用户问题的不同子问题。\n`

	// 用户提示
	user := fmt.Sprintf("当前日期：%s\n\n用户问题：%s\n\n已知资料：\n%s\n", time.Now().Format("2006-01-02"), question, refText)

	// 调用大模型
	resp, err := r.LLM.ChatCompletion(ctx, model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, intPtr(256), floatPtr(0.2))
	if err != nil {
		// 大模型不可用时，使用原问题作为关键词
		logger.Warnf("[TRACE] research.thinkKeywords llm_failed err=%v", err)
		errEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.error", internalproto.StepStateError, "大模型不可用，退化为使用原问题作为检索关键词")
		errEv.Round = round
		errEv.Model = model
		r.report(ctx, errEv)
		return []string{question}, false, nil
	}
	// 处理大模型响应
	out := strings.TrimSpace(resp)
	out = strings.Trim(out, "\"'“”")
	if out == "" {
		return nil, false, nil
	}
	// 如果大模型判断无需检索
	if strings.Contains(out, "无需检索") || strings.Contains(out, "不需要检索") {
		endEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.end", internalproto.StepStateEnd, "大模型判断：无需继续检索")
		endEv.Round = round
		endEv.Model = model
		r.report(ctx, endEv)
		return nil, true, nil
	}
	// 分割关键词
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
	// 报告关键词生成完成
	endEv := internalproto.NewStepEvent("research", "llm", "research.llm.think_keywords.end", internalproto.StepStateEnd, fmt.Sprintf("生成检索关键词 %d 个", len(keywords)))
	endEv.Round = round
	endEv.Model = model
	r.report(ctx, endEv)
	return keywords, false, nil
}

// search 执行搜索
// ctx: 上下文
// round: 轮次
// keywords: 关键词
// 返回搜索结果和可能的错误
func (r *Runner) search(ctx context.Context, round int, keywords []string) ([]tools.Result, error) {
	// 检查Tavily客户端是否初始化
	if r.Tavily == nil {
		return nil, fmt.Errorf("tavily client is nil")
	}
	// 初始化结果切片
	all := make([]tools.Result, 0, len(keywords)*r.Opt.MaxResultsPerQuery)
	// 对每个关键词执行搜索
	for _, q := range keywords {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		// 报告搜索开始
		startEv := internalproto.NewStepEvent("research", "tool", "tavily.search.start", internalproto.StepStateStart, fmt.Sprintf("调用 Tavily 检索：%s", q))
		startEv.Round = round
		startEv.Keyword = q
		r.report(ctx, startEv)
		// 执行搜索
		started := time.Now()
		rsp, err := r.Tavily.Search(ctx, &tools.SearchRequest{Query: q, SearchDepth: r.Opt.SearchDepth, MaxResults: r.Opt.MaxResultsPerQuery})
		if err != nil {
			// 报告搜索失败
			errEv := internalproto.NewStepEvent("research", "tool", "tavily.search.error", internalproto.StepStateError, fmt.Sprintf("Tavily 检索失败：%s", q))
			errEv.Round = round
			errEv.Keyword = q
			r.report(ctx, errEv)
			return nil, err
		}
		// 报告搜索完成
		endEv := internalproto.NewStepEvent("research", "tool", "tavily.search.end", internalproto.StepStateEnd, fmt.Sprintf("Tavily 检索完成：%s（%d 条，耗时 %s）", q, len(rsp.Results), time.Since(started).Truncate(10*time.Millisecond)))
		endEv.Round = round
		endEv.Keyword = q
		r.report(ctx, endEv)
		// 添加搜索结果
		all = append(all, rsp.Results...)
	}
	return all, nil
}

// summarize 生成总结
// ctx: 上下文
// question: 问题
// evidence: 证据
// 返回总结和可能的错误
func (r *Runner) summarize(ctx context.Context, question string, evidence []Evidence) (string, error) {
	// 如果没有配置大模型，使用回退总结
	if r.LLM == nil {
		return fallbackSummary(question, evidence), nil
	}
	// 获取模型名称
	model := strings.TrimSpace(r.Models.Chat)
	if model == "" {
		model = strings.TrimSpace(r.Models.Reasoning)
	}
	// 如果没有配置模型，使用回退总结
	if model == "" {
		return fallbackSummary(question, evidence), nil
	}

	// 系统提示
	system := `你是一个中文信息整合与写作助手，必须严格基于提供的Sources回答，不允许编造。\n` +
		`写作要求：\n` +
		`- 先给 TL;DR（2~4行）。\n` +
		`- 正文按用户问题的子主题分节（用清晰的小标题）。\n` +
		`- 任何事实性陈述后面必须带引用编号，如[1]或[1][3]，编号只能来自Sources列表。\n` +
		`- 若Sources不足以回答某部分，必须在"信息缺口与下一步"中明确指出缺口，并说明还需要检索什么。\n` +
		`- 不要输出Sources列表（我会在外部追加）。\n`

	// 用户提示
	user := fmt.Sprintf("当前日期：%s\n\n用户问题：%s\n\nSources（可引用）：\n%s\n", time.Now().Format("2006-01-02"), question, renderSourcesForPrompt(evidence))

	// 调用大模型
	resp, err := r.LLM.ChatCompletion(ctx, model, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, intPtr(900), floatPtr(0.3))
	if err != nil {
		// 大模型不可用时，使用回退总结
		logger.Warnf("[TRACE] research.summarize llm_failed err=%v", err)
		return fallbackSummary(question, evidence), nil
	}
	return strings.TrimSpace(resp), nil
}

// cleanAndRank 清理和排序证据
// all: 所有证据
// 返回清理和排序后的证据
func cleanAndRank(all []Evidence) []Evidence {
	// 初始化输出和去重映射
	out := make([]Evidence, 0, len(all))
	seen := map[string]bool{}
	// 去重
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
		// 清理字段
		e.Title = strings.TrimSpace(e.Title)
		e.URL = strings.TrimSpace(e.URL)
		e.Content = strings.TrimSpace(e.Content)
		out = append(out, e)
	}

	// 按分数排序
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	// 限制数量
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

// mergeWebEvidence 合并网络证据
// base: 基础证据
// results: 搜索结果
// 返回合并后的证据
func mergeWebEvidence(base []Evidence, results []tools.Result) []Evidence {
	// 初始化输出
	out := make([]Evidence, 0, len(base)+len(results))
	out = append(out, base...)
	// 转换搜索结果为证据
	for _, r := range results {
		ev := Evidence{Title: r.Title, URL: r.URL, Content: r.Content, Score: r.Score, Source: "tavily"}
		out = append(out, ev)
	}
	return out
}

// buildEvidenceForPrompt 构建证据提示
// evidence: 证据
// limit: 限制数量
// 返回构建的提示
func buildEvidenceForPrompt(evidence []Evidence, limit int) string {
	if len(evidence) == 0 {
		return "(空)"
	}
	if limit <= 0 {
		limit = 6
	}
	var b strings.Builder
	shown := 0
	// 构建证据列表
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

// renderSourcesForPrompt 渲染提示的来源
// evidence: 证据
// 返回渲染后的来源
func renderSourcesForPrompt(evidence []Evidence) string {
	if len(evidence) == 0 {
		return "(无)"
	}
	var b strings.Builder
	// 构建来源列表
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

// renderSources 渲染来源
// evidence: 证据
// 返回渲染后的来源
func renderSources(evidence []Evidence) string {
	var b strings.Builder
	b.WriteString("Sources:\n")
	if len(evidence) == 0 {
		b.WriteString("(无)\n")
		return strings.TrimSpace(b.String())
	}
	// 构建来源列表
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

// renderSearchProcess 渲染搜索过程
// rounds: 轮次日志
// seed: 种子证据
// 返回渲染后的搜索过程
func renderSearchProcess(rounds []RoundLog, seed []Evidence) string {
	var b strings.Builder
	b.WriteString("检索过程：\n")
	// 显示已知资料
	if len(seed) > 0 {
		b.WriteString("- 已知资料：已提供（将其作为Sources的一部分）\n")
	} else {
		b.WriteString("- 已知资料：无\n")
	}
	// 如果没有轮次日志，显示未执行检索
	if len(rounds) == 0 {
		b.WriteString("- 未执行联网检索\n")
		return strings.TrimSpace(b.String())
	}
	// 显示每轮的检索情况
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

// shorten 缩短字符串
// s: 字符串
// maxRunes: 最大字符数
// 返回缩短后的字符串
func shorten(s string, maxRunes int) string {
	// 清理字符串
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	// 如果长度不超过限制，直接返回
	if maxRunes <= 0 || len(r) <= maxRunes {
		return s
	}
	// 缩短并添加省略号
	return string(r[:maxRunes]) + "..."
}

// getMainConfigSafe 安全获取主配置
// 返回主配置
func getMainConfigSafe() (cfg *config.MainConfig) {
	// 捕获 panic
	defer func() {
		if r := recover(); r != nil {
			cfg = nil
		}
	}()
	return config.GetMainConfig()
}

// fallbackSummary 回退总结
// question: 问题
// evidence: 证据
// 返回回退总结
func fallbackSummary(question string, evidence []Evidence) string {
	question = strings.TrimSpace(question)
	var b strings.Builder
	// 添加 TL;DR
	b.WriteString("TL;DR\n")
	if len(evidence) == 0 {
		b.WriteString("- 当前没有可用Sources，无法给出可靠结论。[1]\n")
	} else {
		fmt.Fprintf(&b, "- 基于当前检索到的%v条Sources，已整理出要点与可回答范围。[1]\n", len(evidence))
	}

	// 添加核心要点
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

	// 添加信息缺口与下一步
	b.WriteString("\n信息缺口与下一步\n")
	if len(evidence) == 0 {
		b.WriteString("- 缺口：没有任何可引用来源。下一步：提供链接/指定范围，或启用联网检索以获取Sources。[1]\n")
		return strings.TrimSpace(b.String())
	}
	b.WriteString("- 若需要更全面回答，请补充更具体的子问题（时间范围/版本/地区/对象），或允许更多轮次检索以补齐Sources。[1]\n")
	_ = question
	return strings.TrimSpace(b.String())
}

// intPtr 返回整数指针
// v: 整数
// 返回整数指针
func intPtr(v int) *int { return &v }

// floatPtr 返回浮点数指针
// v: 浮点数
// 返回浮点数指针
func floatPtr(v float64) *float64 { return &v }

// init 初始化函数
func init() {
	logger.Infof("[TRACE] research.loop loaded")
}
