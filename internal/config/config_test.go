package config

import (
	"testing"
)

func TestForgeConfig_Defaults(t *testing.T) {
	// No FORGE_* env vars set — check defaults are sane.
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Forge.Enabled {
		t.Error("FORGE_ENABLED should default to false")
	}
	if cfg.Forge.EmbedModel != "qwen3-embedding:8b" {
		t.Errorf("unexpected default embed model: %q", cfg.Forge.EmbedModel)
	}
	if cfg.Forge.Timeout != 300 {
		t.Errorf("unexpected default timeout: %d", cfg.Forge.Timeout)
	}
}

func TestForgeConfig_EnvOverrides(t *testing.T) {
	t.Setenv("FORGE_ENABLED", "true")
	t.Setenv("FORGE_EMBED_MODEL", "custom-embed:1b")
	t.Setenv("FORGE_CHAT_MODEL", "nemotron-3-nano:30b-a3b-q4_K_M")
	t.Setenv("FORGE_TIMEOUT", "600")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !cfg.Forge.Enabled {
		t.Error("FORGE_ENABLED=true not picked up")
	}
	if cfg.Forge.EmbedModel != "custom-embed:1b" {
		t.Errorf("unexpected embed model: %q", cfg.Forge.EmbedModel)
	}
	if cfg.Forge.ChatModel != "nemotron-3-nano:30b-a3b-q4_K_M" {
		t.Errorf("unexpected chat model: %q", cfg.Forge.ChatModel)
	}
	if cfg.Forge.Timeout != 600 {
		t.Errorf("unexpected timeout: %d", cfg.Forge.Timeout)
	}
}

func TestForgeConfig_ChatModelDefaultsToOllamaModel(t *testing.T) {
	t.Setenv("OLLAMA_MODEL", "llama3:8b")
	// FORGE_CHAT_MODEL not set — should fall back to OLLAMA_MODEL
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Forge.ChatModel != "llama3:8b" {
		t.Errorf("expected chat model to fall back to OLLAMA_MODEL, got %q", cfg.Forge.ChatModel)
	}
}
