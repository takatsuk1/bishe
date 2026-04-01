package main

import (
	"ai/pkg/protocol"
	"ai/pkg/transport/httpagent"
	"context"
	"fmt"
	"strings"
	"time"
)

type smokeCase struct {
	name    string
	baseURL string
	input   string
	check   func(string) error
}

type smokeResult struct {
	name     string
	taskID   string
	status   protocol.TaskState
	output   string
	err      error
	duration time.Duration
}

func main() {
	cases := []smokeCase{
		{
			name:    "deepresearch",
			baseURL: "http://127.0.0.1:9993",
			input:   "帮我搜索重庆邮电大学的相关信息，并给出简介、优势学科和校园位置。",
			check: func(out string) error {
				trimmed := strings.TrimSpace(out)
				if trimmed == "" {
					return fmt.Errorf("empty output")
				}
				if strings.EqualFold(trimmed, "true") || strings.EqualFold(trimmed, "false") {
					return fmt.Errorf("output is only boolean: %q", trimmed)
				}
				if len([]rune(trimmed)) < 20 {
					return fmt.Errorf("output too short: %q", trimmed)
				}
				return nil
			},
		},
		{
			name:    "urlreader",
			baseURL: "http://127.0.0.1:9991",
			input:   "请读取并总结这个网页：https://www.cqupt.edu.cn/",
			check: func(out string) error {
				trimmed := strings.TrimSpace(out)
				if trimmed == "" {
					return fmt.Errorf("empty output")
				}
				if strings.Contains(strings.ToLower(trimmed), "no valid url") {
					return fmt.Errorf("url extraction failed: %q", trimmed)
				}
				return nil
			},
		},
		{
			name:    "lbshelper",
			baseURL: "http://127.0.0.1:9992",
			input:   "帮我规划从重庆北站到重庆邮电大学的路线，优先地铁+步行。",
			check: func(out string) error {
				trimmed := strings.TrimSpace(out)
				if trimmed == "" {
					return fmt.Errorf("empty output")
				}
				if strings.Contains(strings.ToLower(trimmed), "empty") {
					return fmt.Errorf("looks empty: %q", trimmed)
				}
				return nil
			},
		},
		{
			name:    "host",
			baseURL: "http://127.0.0.1:8080",
			input:   "请用可调用的agent能力，帮我总结重庆邮电大学官网信息。",
			check: func(out string) error {
				trimmed := strings.TrimSpace(out)
				if trimmed == "" {
					return fmt.Errorf("empty output")
				}
				return nil
			},
		},
	}

	results := make([]smokeResult, 0, len(cases))
	for _, tc := range cases {
		res := runCase(tc)
		results = append(results, res)
	}

	fmt.Println("==== real agent smoke test summary ====")
	failed := 0
	for _, r := range results {
		if r.err != nil {
			failed++
			fmt.Printf("[FAIL] %s task=%s state=%s dur=%s err=%v\\n", r.name, r.taskID, r.status, r.duration, r.err)
			continue
		}
		preview := strings.ReplaceAll(r.output, "\n", " ")
		if len([]rune(preview)) > 120 {
			preview = string([]rune(preview)[:120]) + "..."
		}
		fmt.Printf("[PASS] %s task=%s state=%s dur=%s output=%q\\n", r.name, r.taskID, r.status, r.duration, preview)
	}

	if failed > 0 {
		panic(fmt.Sprintf("%d smoke cases failed", failed))
	}
}

func runCase(tc smokeCase) smokeResult {
	start := time.Now()
	res := smokeResult{name: tc.name}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	cli := httpagent.NewClient(tc.baseURL, 4*time.Minute)
	taskID, err := cli.SendMessage(ctx, protocol.Message{
		Role:  protocol.MessageRoleUser,
		Parts: []protocol.Part{protocol.NewTextPart(tc.input)},
	})
	if err != nil {
		res.err = fmt.Errorf("send message failed: %w", err)
		res.duration = time.Since(start)
		return res
	}
	res.taskID = taskID

	events, errs := cli.StreamTaskEvents(ctx, taskID)
	var finalState protocol.TaskState
	var out strings.Builder
	for ev := range events {
		if ev.TaskStatusUpdate == nil {
			continue
		}
		finalState = ev.TaskStatusUpdate.Status.State
		if msg := ev.TaskStatusUpdate.Status.Message; msg != nil {
			text := strings.TrimSpace(msg.FirstText())
			if text != "" {
				if out.Len() > 0 {
					out.WriteString("\n")
				}
				out.WriteString(text)
			}
		}
	}
	if err = <-errs; err != nil {
		res.err = fmt.Errorf("stream failed: %w", err)
		res.duration = time.Since(start)
		return res
	}

	res.status = finalState
	res.output = strings.TrimSpace(out.String())
	if finalState != protocol.TaskStateCompleted {
		res.err = fmt.Errorf("unexpected terminal state: %s", finalState)
		res.duration = time.Since(start)
		return res
	}
	if tc.check != nil {
		if err = tc.check(res.output); err != nil {
			res.err = err
			res.duration = time.Since(start)
			return res
		}
	}

	res.duration = time.Since(start)
	return res
}
