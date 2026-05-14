package main

import (
	chatapi "ai/api/chat"
	"ai/config"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type chatPerfResult struct {
	Name           string        `json:"name"`
	Method         string        `json:"method"`
	Path           string        `json:"path"`
	Model          string        `json:"model"`
	Concurrency    int           `json:"concurrency"`
	Duration       time.Duration `json:"duration"`
	TotalRequests  int64         `json:"totalRequests"`
	Successes      int64         `json:"successes"`
	Failures       int64         `json:"failures"`
	RequestsPerSec float64       `json:"requestsPerSec"`
	AvgLatencyMs   float64       `json:"avgLatencyMs"`
	P50LatencyMs   float64       `json:"p50LatencyMs"`
	P95LatencyMs   float64       `json:"p95LatencyMs"`
	P99LatencyMs   float64       `json:"p99LatencyMs"`
	MaxLatencyMs   float64       `json:"maxLatencyMs"`
}

type authEnvelope struct {
	User struct {
		UserID string `json:"userId"`
	} `json:"user"`
	Tokens struct {
		AccessToken string `json:"accessToken"`
	} `json:"tokens"`
	Error string `json:"error"`
}

func main() {
	var (
		baseURL     string
		model       string
		prompt      string
		concurrency int
		duration    time.Duration
		timeout     time.Duration
		stream      bool
		maxTokens   int
		temperature float64
		username    string
		password    string
		displayName string
	)

	flag.StringVar(&baseURL, "base-url", "", "chat entrypoint base URL; defaults to config.openai_connector.listen or http://127.0.0.1:11000")
	flag.StringVar(&model, "model", "host", "chat model / agent name to call")
	flag.StringVar(&prompt, "prompt", "请用中文简洁解释什么是大模型压测，并给出一个一句话结论。", "single prompt to send")
	flag.IntVar(&concurrency, "concurrency", 1, "number of concurrent workers")
	flag.DurationVar(&duration, "duration", 9*time.Minute, "benchmark duration")
	flag.DurationVar(&timeout, "timeout", 20*time.Minute, "per-request timeout")
	flag.BoolVar(&stream, "stream", true, "use streaming chat completion")
	flag.IntVar(&maxTokens, "max-tokens", 256, "max tokens")
	flag.Float64Var(&temperature, "temperature", 0.0, "sampling temperature")
	flag.StringVar(&username, "username", "", "optional username; if empty a unique perf user is created")
	flag.StringVar(&password, "password", "Perf123456", "password used for register/login")
	flag.StringVar(&displayName, "display-name", "Real Chat Perf User", "display name used for register")
	flag.Parse()

	config.CmdlineFlags.MainConfigFilename = "config.yaml"
	config.Init()
	cfg := config.GetMainConfig()
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "config init failed: main config is nil")
		os.Exit(1)
	}

	if strings.TrimSpace(baseURL) == "" {
		baseURL = normalizeBaseURL(cfg.OpenAIConnector.Listen)
		if baseURL == "" {
			baseURL = "http://127.0.0.1:11000"
		}
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.TrimSpace(model) == "" {
		model = "host"
	}

	if strings.TrimSpace(username) == "" {
		username = fmt.Sprintf("real_chat_perf_%d", time.Now().UnixNano())
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	token, err := ensureAccessToken(client, baseURL, username, password, displayName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth bootstrap failed: %v\n", err)
		os.Exit(1)
	}

	maxTokensPtr := &maxTokens
	temperaturePtr := &temperature
	streamPtr := &stream

	fmt.Printf("base_url=%s\n", baseURL)
	fmt.Printf("model=%s\n", model)
	fmt.Printf("stream=%t\n", stream)
	fmt.Printf("concurrency=%d\n", concurrency)
	fmt.Printf("duration=%s\n", duration)
	fmt.Printf("timeout=%s\n", timeout)
	fmt.Printf("prompt_len=%d\n", len([]rune(prompt)))
	fmt.Printf("username=%s\n", username)

	result, err := runChatLoad(client, baseURL, model, token, prompt, streamPtr, maxTokensPtr, temperaturePtr, concurrency, duration)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load test failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Printf("Load test timestamp: %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("Profile: duration=%s concurrency=%d stream=%t\n", duration, concurrency, stream)
	fmt.Println()
	fmt.Println("RESULTS_JSON_START")
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode([]chatPerfResult{result})
	fmt.Println("RESULTS_JSON_END")
	fmt.Println()
	printChatSummaryTable([]chatPerfResult{result})
}

func ensureAccessToken(client *http.Client, baseURL, username, password, displayName string) (string, error) {
	registerURL := joinURL(baseURL, "/v1/auth/register")
	loginURL := joinURL(baseURL, "/v1/auth/login")

	tryLogin := func() (string, error) {
		payload, _ := json.Marshal(map[string]string{
			"username": username,
			"password": password,
		})
		return authRequest(client, loginURL, payload)
	}

	payload, _ := json.Marshal(map[string]string{
		"username":    username,
		"password":    password,
		"displayName": displayName,
	})
	token, err := authRequest(client, registerURL, payload)
	if err == nil {
		return token, nil
	}

	if token, loginErr := tryLogin(); loginErr == nil {
		return token, nil
	}
	return "", fmt.Errorf("register and login both failed: register=%v", err)
}

func authRequest(client *http.Client, endpoint string, payload []byte) (string, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var parsed authEnvelope
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse auth response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		if parsed.Error != "" {
			return "", fmt.Errorf("auth endpoint returned %d: %s", resp.StatusCode, parsed.Error)
		}
		return "", fmt.Errorf("auth endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(parsed.Tokens.AccessToken) == "" {
		return "", fmt.Errorf("auth endpoint returned empty access token")
	}
	return parsed.Tokens.AccessToken, nil
}

func runChatLoad(client *http.Client, baseURL, model, token, prompt string, stream *bool, maxTokens *int, temperature *float64, concurrency int, duration time.Duration) (chatPerfResult, error) {
	if concurrency <= 0 {
		return chatPerfResult{}, fmt.Errorf("concurrency must be positive")
	}
	if duration <= 0 {
		return chatPerfResult{}, fmt.Errorf("duration must be positive")
	}

	stopAt := time.Now().Add(duration)
	latencies := make([]time.Duration, 0, 256)
	var latMu sync.Mutex
	var totalReq int64
	var okReq int64
	var failReq int64
	var totalLatencyNs int64

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			reqSeq := 0
			for time.Now().Before(stopAt) {
				reqSeq++
				payload, _ := json.Marshal(chatapi.ChatCompletionRequest{
					Model:          model,
					ConversationID: fmt.Sprintf("real-chat-perf-%d-%d", worker, reqSeq),
					Messages: []chatapi.Message{{
						Role:    "user",
						Content: prompt,
					}},
					Stream:      *stream,
					MaxTokens:   maxTokens,
					Temperature: temperature,
				})

				req, err := http.NewRequest(http.MethodPost, joinURL(baseURL, "/v1/chat/completions"), bytes.NewReader(payload))
				if err != nil {
					atomic.AddInt64(&failReq, 1)
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+token)

				start := time.Now()
				resp, err := client.Do(req)
				latency := time.Since(start)

				atomic.AddInt64(&totalReq, 1)
				atomic.AddInt64(&totalLatencyNs, latency.Nanoseconds())
				latMu.Lock()
				latencies = append(latencies, latency)
				latMu.Unlock()

				if err != nil {
					atomic.AddInt64(&failReq, 1)
					continue
				}

				_, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()

				if resp.StatusCode == http.StatusOK {
					atomic.AddInt64(&okReq, 1)
				} else {
					atomic.AddInt64(&failReq, 1)
				}
			}
		}(i)
	}
	wg.Wait()

	total := atomic.LoadInt64(&totalReq)
	okCount := atomic.LoadInt64(&okReq)
	failCount := atomic.LoadInt64(&failReq)
	if total == 0 {
		return chatPerfResult{}, fmt.Errorf("no requests recorded")
	}

	latMu.Lock()
	sorted := append([]time.Duration(nil), latencies...)
	latMu.Unlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	avgLatencyMs := float64(atomic.LoadInt64(&totalLatencyNs)) / float64(total) / float64(time.Millisecond)
	return chatPerfResult{
		Name:           "real_chat_completion",
		Method:         http.MethodPost,
		Path:           "/v1/chat/completions",
		Model:          model,
		Concurrency:    concurrency,
		Duration:       duration,
		TotalRequests:  total,
		Successes:      okCount,
		Failures:       failCount,
		RequestsPerSec: float64(total) / duration.Seconds(),
		AvgLatencyMs:   avgLatencyMs,
		P50LatencyMs:   percentileMs(sorted, 0.50),
		P95LatencyMs:   percentileMs(sorted, 0.95),
		P99LatencyMs:   percentileMs(sorted, 0.99),
		MaxLatencyMs:   float64(sorted[len(sorted)-1]) / float64(time.Millisecond),
	}, nil
}

func joinURL(baseURL, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return path
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

func normalizeBaseURL(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return ""
	}
	if strings.HasPrefix(listen, "http://") || strings.HasPrefix(listen, "https://") {
		return strings.TrimRight(listen, "/")
	}
	if strings.HasPrefix(listen, ":") {
		return "http://127.0.0.1" + listen
	}
	if u, err := url.Parse(listen); err == nil && u.Scheme != "" && u.Host != "" {
		return strings.TrimRight(listen, "/")
	}
	return "http://127.0.0.1:" + strings.TrimLeft(listen, ":")
}

func percentileMs(values []time.Duration, ratio float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if ratio <= 0 {
		return float64(values[0]) / float64(time.Millisecond)
	}
	if ratio >= 1 {
		return float64(values[len(values)-1]) / float64(time.Millisecond)
	}
	index := int(float64(len(values)-1) * ratio)
	return float64(values[index]) / float64(time.Millisecond)
}

func printChatSummaryTable(results []chatPerfResult) {
	fmt.Printf("%-24s %-8s %-8s %-8s %-10s %-10s %-10s %-10s %-10s\n",
		"endpoint", "req", "ok", "fail", "qps", "avg_ms", "p95_ms", "p99_ms", "max_ms")
	for _, r := range results {
		fmt.Printf("%-24s %-8d %-8d %-8d %-10.2f %-10.2f %-10.2f %-10.2f %-10.2f\n",
			r.Name, r.TotalRequests, r.Successes, r.Failures, r.RequestsPerSec, r.AvgLatencyMs, r.P95LatencyMs, r.P99LatencyMs, r.MaxLatencyMs)
	}
}
