package main

import (
	"ai/config"
	"ai/pkg/llm"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		prompt      string
		model       string
		useStream   bool
		timeoutSec  int
		showReply   bool
		maxTokens   int
		temperature float64
	)

	flag.StringVar(&prompt, "prompt", "请只回复“ok”。", "single prompt to send")
	flag.StringVar(&model, "model", "", "override model name; defaults to llm.chat_model")
	flag.BoolVar(&useStream, "stream", false, "use streaming chat completion")
	flag.IntVar(&timeoutSec, "timeout", 180, "request timeout in seconds")
	flag.BoolVar(&showReply, "show-reply", true, "print model reply")
	flag.IntVar(&maxTokens, "max-tokens", 128, "max tokens")
	flag.Float64Var(&temperature, "temperature", 0.0, "sampling temperature")
	flag.Parse()

	config.CmdlineFlags.MainConfigFilename = "config.yaml"
	config.Init()
	cfg := config.GetMainConfig()
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "config init failed: main config is nil")
		os.Exit(1)
	}

	baseURL := strings.TrimSpace(cfg.LLM.URL)
	apiKey := strings.TrimSpace(cfg.LLM.APIKey)
	if strings.TrimSpace(model) == "" {
		model = strings.TrimSpace(cfg.LLM.ChatModel)
	}
	if strings.TrimSpace(model) == "" {
		model = strings.TrimSpace(cfg.LLM.ReasoningModel)
	}

	if baseURL == "" || strings.TrimSpace(model) == "" {
		fmt.Fprintf(os.Stderr, "invalid llm config: url=%q model=%q\n", baseURL, model)
		os.Exit(1)
	}

	client := llm.NewClient(baseURL, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	maxTokensPtr := &maxTokens
	temperaturePtr := &temperature

	fmt.Printf("mode=%s\n", ternary(useStream, "stream", "non-stream"))
	fmt.Printf("url=%s\n", baseURL)
	fmt.Printf("model=%s\n", model)
	fmt.Printf("prompt_len=%d\n", len([]rune(prompt)))
	fmt.Printf("timeout_sec=%d\n", timeoutSec)

	start := time.Now()
	if !useStream {
		resp, err := client.ChatCompletion(ctx, model, []llm.Message{{Role: "user", Content: prompt}}, maxTokensPtr, temperaturePtr)
		elapsed := time.Since(start)
		if err != nil {
			fmt.Fprintf(os.Stderr, "status=error\nelapsed_ms=%d\nerror=%v\n", elapsed.Milliseconds(), err)
			os.Exit(1)
		}
		fmt.Printf("status=ok\nelapsed_ms=%d\nresponse_len=%d\n", elapsed.Milliseconds(), len([]rune(resp)))
		if showReply {
			fmt.Printf("response=%s\n", strings.TrimSpace(resp))
		}
		return
	}

	var (
		firstDeltaAt time.Duration
		fullResp     strings.Builder
	)
	_, err := client.ChatCompletionStream(ctx, model, []llm.Message{{Role: "user", Content: prompt}}, maxTokensPtr, temperaturePtr, func(delta string) error {
		if firstDeltaAt == 0 && strings.TrimSpace(delta) != "" {
			firstDeltaAt = time.Since(start)
		}
		fullResp.WriteString(delta)
		return nil
	})
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status=error\nelapsed_ms=%d\nerror=%v\n", elapsed.Milliseconds(), err)
		os.Exit(1)
	}

	fmt.Printf("status=ok\n")
	if firstDeltaAt > 0 {
		fmt.Printf("first_delta_ms=%d\n", firstDeltaAt.Milliseconds())
	} else {
		fmt.Printf("first_delta_ms=-1\n")
	}
	resp := strings.TrimSpace(fullResp.String())
	fmt.Printf("elapsed_ms=%d\nresponse_len=%d\n", elapsed.Milliseconds(), len([]rune(resp)))
	if showReply {
		fmt.Printf("response=%s\n", resp)
	}
}

func ternary[T any](cond bool, left, right T) T {
	if cond {
		return left
	}
	return right
}
