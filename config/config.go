package config

import (
	"fmt"
	"os"
	"sync/atomic"

	"ai/pkg/logger"

	"gopkg.in/yaml.v3"
)

var mainConfigValue atomic.Value

// CmdlineFlags 命令行参数
var CmdlineFlags = struct {
	ConfigProvider     string
	MainConfigFilename string
	TRPCConfig         string
}{}

// MainConfig 服务配置
type MainConfig struct {
	Tavily struct {
		APIKey string `yaml:"apikey"`
	} `yaml:"tavily"`
	AMap struct {
		ServerURL string `yaml:"server_url"`
	} `yaml:"amap"`
	Jina struct {
		APIKey string `yaml:"apikey"`
	} `yaml:"jina"`
	Dash struct {
		APIKey string `yaml:"apikey"`
	} `yaml:"dashscope"`
	EdgeOnePages struct {
		ServerURL string `yaml:"server_url"`
	} `yaml:"edge_one_pages"`
	LLM struct {
		URL            string `yaml:"url"`
		APIKey         string `yaml:"apikey"`
		IntentModel    string `yaml:"intent_model"`
		ChatModel      string `yaml:"chat_model"`
		ReasoningModel string `yaml:"reasoning_model"`
		VideoModel     string `yaml:"video_model"`
		IndexModel     string `yaml:"index_model"`
	} `yaml:"llm"`
	Langfuse struct {
		Name      string `yaml:"name"`
		Host      string `yaml:"host"`
		PublicKey string `yaml:"public_key"`
		SecretKey string `yaml:"secret_key"`
	} `yaml:"langfuse"`
	Redis struct {
		URL           string `yaml:"url"`
		MaxWindowSize int    `yaml:"max_window_size"`
	} `yaml:"redis"`
	MySQL struct {
		DSN string `yaml:"dsn"`
	} `yaml:"mysql"`
	OpenAIConnector struct {
		Listen string        `yaml:"listen"`
		Agents []AgentConfig `yaml:"agents"`
	} `yaml:"openai_connector"`
	QQBotConnector struct {
		AppID  string      `yaml:"appid"`
		Secret string      `yaml:"secret"`
		Agent  AgentConfig `yaml:"agent"`
	} `yaml:"qqbot"`
	HostAgent struct {
		Agents []AgentConfig `yaml:"agents"`
	} `yaml:"host_agent"`
	Orchestrator struct {
		Enable                bool   `yaml:"enable"`
		Engine                string `yaml:"engine"`
		CompatibilityMode     string `yaml:"compatibility_mode"`
		DefaultTaskTimeoutSec int    `yaml:"default_task_timeout_sec"`
		Retry                 struct {
			MaxAttempts      int  `yaml:"max_attempts"`
			BaseBackoffMs    int  `yaml:"base_backoff_ms"`
			MaxBackoffMs     int  `yaml:"max_backoff_ms"`
			EnableDeadLetter bool `yaml:"enable_dead_letter"`
		} `yaml:"retry"`
	} `yaml:"orchestrator"`
	Auth struct {
		JWTSecret             string `yaml:"jwt_secret"`
		AccessTokenTTLMinutes int    `yaml:"access_token_ttl_minutes"`
		RefreshTokenTTLHours  int    `yaml:"refresh_token_ttl_hours"`
	} `yaml:"auth"`
}

type AgentConfig struct {
	Name      string `yaml:"name"`
	ServerURL string `yaml:"server_url"`
	JwksURL   string `yaml:"jwks_url"`
	CardURL   string `yaml:"card_url"`
}

// Init 初始化配置
func Init() {
	LoadConfig(CmdlineFlags.ConfigProvider, "yaml",
		CmdlineFlags.MainConfigFilename, &MainConfig{}, &mainConfigValue)
}

// GetMainConfig 获取服务配置
func GetMainConfig() *MainConfig {
	return mainConfigValue.Load().(*MainConfig)
}

// LoadConfig 加载并监听一个配置文件，失败则 panic，filename 默认是 config 目录下的
func LoadConfig(provider string, unmarshalName string, filename string, dst interface{}, cfg *atomic.Value) {
	_ = provider
	_ = unmarshalName
	LoadConfigWithCallback(filename, dst, nil)
	cfg.Store(dst)
}

// LoadConfigWithCallback 加载一个配置文件（当前实现不支持监听热更新），失败则 panic。
func LoadConfigWithCallback(filename string, dst interface{}, callback func(path string, data []byte)) {
	buf, err := os.ReadFile(filename)
	logger.Infof("Attempting to read file: %s", filename)
	if err != nil {
		panic(fmt.Errorf("err=%v, filename=%s", err, filename))
	}
	if err = yaml.Unmarshal(buf, dst); err != nil {
		panic(err)
	}
	logger.Infof("set config:%+v", dst)
	if callback != nil {
		callback(filename, buf)
	}
}
