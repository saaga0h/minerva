package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	App         AppConfig         `json:"app"`
	Log         LogConfig         `json:"log"`
	Database    DatabaseConfig    `json:"database"`
	Store       StoreConfig       `json:"store"`
	FreshRSS    FreshRSSConfig    `json:"fresh_rss"`
	Linkwarden  LinkwardenConfig  `json:"linkwarden"`
	Ollama      OllamaConfig      `json:"ollama"`
	SearXNG     SearXNGConfig     `json:"searxng"`
	OpenLibrary OpenLibraryConfig `json:"openlibrary"`
	Extractor   ExtractorConfig   `json:"extractor"`
	Koha        KohaConfig        `json:"koha"`
	Ntfy        NtfyConfig        `json:"ntfy"`
}

type AppConfig struct {
	Name        string `json:"name" env:"APP_NAME" default:"minerva"`
	Version     string `json:"version" env:"APP_VERSION" default:"1.0.0"`
	Env         string `json:"env" env:"APP_ENV" default:"production"`
	DebugOllama bool   `env:"DEBUG_OLLAMA" envDefault:"false"`
}

type LogConfig struct {
	Level  string `json:"level" env:"LOG_LEVEL" default:"info"`
	Format string `json:"format" env:"LOG_FORMAT" default:"json"`
}

type DatabaseConfig struct {
	Path string `json:"path" env:"DATABASE_PATH" default:"./data/minerva.db"`
}

type StoreConfig struct {
	DSN     string `json:"dsn" env:"STORE_DSN"`
	Enabled bool   `json:"enabled" env:"STORE_ENABLED"`
}

type FreshRSSConfig struct {
	BaseURL string `json:"base_url" env:"FRESHRSS_BASE_URL"`
	APIKey  string `json:"api_key" env:"FRESHRSS_API_KEY"`
	Timeout int    `json:"timeout" env:"FRESHRSS_TIMEOUT" default:"30"`
}

type LinkwardenConfig struct {
	BaseURL string `json:"base_url" env:"LINKWARDEN_BASE_URL"`
	APIKey  string `json:"api_key"  env:"LINKWARDEN_API_KEY"`
	Timeout int    `json:"timeout"  env:"LINKWARDEN_TIMEOUT" default:"30"`
}

type OllamaConfig struct {
	BaseURL     string  `json:"base_url" env:"OLLAMA_BASE_URL" default:"http://localhost:11434"`
	Model       string  `json:"model" env:"OLLAMA_MODEL" default:"llama2"`
	Timeout     int     `json:"timeout" env:"OLLAMA_TIMEOUT" default:"300"`
	MaxTokens   int     `json:"max_tokens" env:"OLLAMA_MAX_TOKENS" default:"2048"`
	Temperature float64 `json:"temperature" env:"OLLAMA_TEMPERATURE" default:"0.7"`
}

type OpenLibraryConfig struct {
	Timeout int `json:"timeout" env:"OPENLIBRARY_TIMEOUT" default:"30"`
}

type SearXNGConfig struct {
	BaseURL string `json:"base_url" env:"SEARXNG_BASE_URL"`
	Timeout int    `json:"timeout" env:"SEARXNG_TIMEOUT" default:"30"`
}

type ExtractorConfig struct {
	UserAgent string `json:"user_agent" env:"EXTRACTOR_USER_AGENT" default:"Minerva/1.0"`
	Timeout   int    `json:"timeout" env:"EXTRACTOR_TIMEOUT" default:"30"`
	MaxSize   int64  `json:"max_size" env:"EXTRACTOR_MAX_SIZE" default:"10485760"` // 10MB
}

type KohaConfig struct {
	BaseURL  string `json:"base_url" env:"KOHA_BASE_URL"`
	Username string `json:"username" env:"KOHA_USERNAME"`
	Password string `json:"password" env:"KOHA_PASSWORD"`
	Timeout  int    `json:"timeout" env:"KOHA_TIMEOUT" default:"30"`
}

type NtfyConfig struct {
	BaseURL  string `json:"base_url" env:"NTFY_BASE_URL" default:"https://ntfy.sh"`
	Topic    string `json:"topic" env:"NTFY_TOPIC"`
	Token    string `json:"token" env:"NTFY_TOKEN"`
	Priority string `json:"priority" env:"NTFY_PRIORITY" default:"default"`
	Enabled  bool   `json:"enabled" env:"NTFY_ENABLED" default:"false"`
}

// Load configuration from environment variables
func Load(configPath string) (*Config, error) {
	// Load .env file if specified
	if configPath != "" {
		if err := godotenv.Load(configPath); err != nil {
			return nil, fmt.Errorf("failed to load env file %s: %w", configPath, err)
		}
	} else {
		// Try to load .env from current directory
		godotenv.Load()
	}

	config := &Config{
		App: AppConfig{
			Name:        getEnv("APP_NAME", "minerva"),
			Version:     getEnv("APP_VERSION", "1.0.0"),
			Env:         getEnv("APP_ENV", "production"),
			DebugOllama: getEnv("DEBUG_OLLAMA", "false") == "true",
		},
		Log: LogConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
		},
		Database: DatabaseConfig{
			Path: getEnv("DATABASE_PATH", "./data/minerva.db"),
		},
		Store: StoreConfig{
			DSN:     getEnv("STORE_DSN", "postgres://minerva:minerva@localhost:5432/minerva"),
			Enabled: getEnv("STORE_ENABLED", "false") == "true",
		},
		FreshRSS: FreshRSSConfig{
			BaseURL: getEnv("FRESHRSS_BASE_URL", ""),
			APIKey:  getEnv("FRESHRSS_API_KEY", ""),
			Timeout: getEnvInt("FRESHRSS_TIMEOUT", 30),
		},
		Linkwarden: LinkwardenConfig{
			BaseURL: getEnv("LINKWARDEN_BASE_URL", ""),
			APIKey:  getEnv("LINKWARDEN_API_KEY", ""),
			Timeout: getEnvInt("LINKWARDEN_TIMEOUT", 30),
		},
		Ollama: OllamaConfig{
			BaseURL:     getEnv("OLLAMA_BASE_URL", "http://localhost:11434"),
			Model:       getEnv("OLLAMA_MODEL", "llama2"),
			Timeout:     getEnvInt("OLLAMA_TIMEOUT", 300),
			MaxTokens:   getEnvInt("OLLAMA_MAX_TOKENS", 2048),
			Temperature: getEnvFloat("OLLAMA_TEMPERATURE", 0.7),
		},
		SearXNG: SearXNGConfig{
			BaseURL: getEnv("SEARXNG_BASE_URL", ""),
			Timeout: getEnvInt("SEARXNG_TIMEOUT", 30),
		},
		OpenLibrary: OpenLibraryConfig{
			Timeout: getEnvInt("OPENLIBRARY_TIMEOUT", 30),
		},
		Extractor: ExtractorConfig{
			UserAgent: getEnv("EXTRACTOR_USER_AGENT", "Minerva/1.0"),
			Timeout:   getEnvInt("EXTRACTOR_TIMEOUT", 30),
			MaxSize:   getEnvInt64("EXTRACTOR_MAX_SIZE", 10485760),
		},
		Koha: KohaConfig{
			BaseURL:  getEnv("KOHA_BASE_URL", ""),
			Username: getEnv("KOHA_USERNAME", ""),
			Password: getEnv("KOHA_PASSWORD", ""),
			Timeout:  getEnvInt("KOHA_TIMEOUT", 30),
		},
		Ntfy: NtfyConfig{
			BaseURL:  getEnv("NTFY_BASE_URL", "https://ntfy.sh"),
			Topic:    getEnv("NTFY_TOPIC", ""),
			Token:    getEnv("NTFY_TOKEN", ""),
			Priority: getEnv("NTFY_PRIORITY", "default"),
			Enabled:  getEnv("NTFY_ENABLED", "false") == "true",
		},
	}

	return config, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := parseInt(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvInt64(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if intValue, err := parseInt64(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := parseFloat(value); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

// Helper functions for parsing
func parseInt(s string) (int, error) {
	var i int
	_, err := fmt.Sscanf(s, "%d", &i)
	return i, err
}

func parseInt64(s string) (int64, error) {
	var i int64
	_, err := fmt.Sscanf(s, "%d", &i)
	return i, err
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}
