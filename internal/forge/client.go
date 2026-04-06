package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/saaga0h/minerva/internal/config"
	"github.com/saaga0h/minerva/internal/services"
	"github.com/sirupsen/logrus"
)

// forgeResponse is the JSON Forge sends to compute/response/{client_id}/{correlation_id}.
type forgeResponse struct {
	CorrelationID string          `json:"correlation_id"`
	JobID         string          `json:"job_id"`
	Success       bool            `json:"success"`
	Result        json.RawMessage `json:"result,omitempty"`
	Error         string          `json:"error,omitempty"`
	DurationMS    int64           `json:"duration_ms,omitempty"`
}

type embedResult struct {
	Embedding []float32 `json:"embedding"`
}

type chatResult struct {
	Response         string `json:"response"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

// BrokerConfig holds the MQTT connection details for the Forge client.
// Callers pass their existing MQTT_BROKER_URL / MQTT_USER / MQTT_PASSWORD
// values — Forge shares the same broker as Minerva.
// ClientID must be unique per primitive (e.g. "forge-search-arxiv") so that
// response subscriptions on compute/response/{client_id}/+ don't overlap.
type BrokerConfig struct {
	BrokerURL string
	ClientID  string
	Username  string
	Password  string
}

// Client routes Ollama calls through the Forge GPU compute queue.
// When FORGE_ENABLED=false it falls back to calling Ollama directly.
type Client struct {
	cfg      config.ForgeConfig
	clientID string
	ollama   *services.Ollama // fallback
	mqtt     paho.Client
	logger   *logrus.Logger

	pending sync.Map // correlationID (string) → chan forgeResponse
}

// New creates a ForgeClient. If cfg.Enabled is false the MQTT connection is
// skipped and all calls fall through to the provided Ollama instance.
func New(cfg config.ForgeConfig, broker BrokerConfig, ollama *services.Ollama, logger *logrus.Logger) (*Client, error) {
	c := &Client{
		cfg:      cfg,
		clientID: broker.ClientID,
		ollama:   ollama,
		logger:   logger,
	}

	if !cfg.Enabled {
		logger.Info("Forge disabled (FORGE_ENABLED=false) — using direct Ollama fallback")
		return c, nil
	}

	opts := paho.NewClientOptions().
		AddBroker(broker.BrokerURL).
		SetClientID(broker.ClientID).
		SetUsername(broker.Username).
		SetPassword(broker.Password).
		SetCleanSession(false). // must be false — responses may arrive after brief reconnect
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(func(_ paho.Client) {
			logger.WithField("broker", broker.BrokerURL).Info("Forge MQTT connected")
			// Re-subscribe on every connect (required when CleanSession=false and broker
			// forgets the subscription after a long disconnect).
			c.subscribeResponses()
		}).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			logger.WithError(err).Warn("Forge MQTT connection lost, reconnecting")
		})

	// Assign c.mqtt before Connect() so that OnConnectHandler (which calls
	// subscribeResponses → c.mqtt.Subscribe) never sees a nil c.mqtt.
	c.mqtt = paho.NewClient(opts)

	token := c.mqtt.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return nil, fmt.Errorf("Forge MQTT connect timeout")
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("Forge MQTT connect: %w", err)
	}

	// Explicitly subscribe here so the initial subscription is synchronous and
	// complete before New() returns. The OnConnectHandler handles re-subscription
	// on reconnects; this call handles the very first connect.
	c.subscribeResponses()

	return c, nil
}

// subscribeResponses subscribes to the wildcard response topic. Called on
// every successful connect so re-subscriptions happen after reconnects.
func (c *Client) subscribeResponses() {
	topic := fmt.Sprintf("compute/response/%s/+", c.clientID)
	token := c.mqtt.Subscribe(topic, 1, c.handleResponse)
	token.Wait()
	if err := token.Error(); err != nil {
		c.logger.WithError(err).Error("Failed to subscribe to Forge response topic")
	} else {
		c.logger.WithField("topic", topic).Info("Subscribed to Forge responses")
	}
}

// handleResponse dispatches an incoming Forge response to the waiting caller.
func (c *Client) handleResponse(_ paho.Client, msg paho.Message) {
	var resp forgeResponse
	if err := json.Unmarshal(msg.Payload(), &resp); err != nil {
		c.logger.WithError(err).Warn("Failed to unmarshal Forge response")
		return
	}

	val, ok := c.pending.Load(resp.CorrelationID)
	if !ok {
		c.logger.WithField("correlation_id", resp.CorrelationID).Warn("Forge response for unknown correlation ID — ignoring")
		return
	}

	ch := val.(chan forgeResponse)
	select {
	case ch <- resp:
	default:
		c.logger.WithField("correlation_id", resp.CorrelationID).Warn("Forge response channel full — dropping")
	}
}

// Embed returns a semantic embedding for text. Synchronous from the caller's
// perspective; the async MQTT round-trip is fully contained here.
func (c *Client) Embed(text string) ([]float32, error) {
	if !c.cfg.Enabled {
		return c.ollama.Embed(text)
	}

	resp, err := c.roundTrip("ollama_embed", map[string]interface{}{
		"model": c.cfg.EmbedModel,
		"input": text,
	})
	if err != nil {
		return nil, err
	}

	var er embedResult
	if err := json.Unmarshal(resp.Result, &er); err != nil {
		return nil, fmt.Errorf("forge embed: unmarshal result: %w", err)
	}
	return er.Embedding, nil
}

// Chat sends a single-turn chat request to Forge and returns the response text.
func (c *Client) Chat(prompt, system, model string, maxTokens int, temperature float64) (string, error) {
	if !c.cfg.Enabled {
		// Fallback: use the Ollama generateCompletion path via direct HTTP.
		// The analyzer passes a fully-constructed prompt, so we use generateCompletion.
		return c.ollamaFallbackChat(prompt)
	}

	resp, err := c.roundTrip("ollama_chat", map[string]interface{}{
		"model":       model,
		"prompt":      prompt,
		"system":      system,
		"temperature": temperature,
		"max_tokens":  maxTokens,
	})
	if err != nil {
		return "", err
	}

	var cr chatResult
	if err := json.Unmarshal(resp.Result, &cr); err != nil {
		return "", fmt.Errorf("forge chat: unmarshal result: %w", err)
	}
	return cr.Response, nil
}

// roundTrip publishes a compute/request message and blocks until the response
// arrives or the configured timeout elapses.
func (c *Client) roundTrip(operation string, payload map[string]interface{}) (*forgeResponse, error) {
	correlationID := fmt.Sprintf("%x", time.Now().UnixNano())

	ch := make(chan forgeResponse, 1)
	c.pending.Store(correlationID, ch)
	defer c.pending.Delete(correlationID)

	topic := fmt.Sprintf("compute/request/%s/%s", c.clientID, correlationID)
	reqBody := map[string]interface{}{
		"operation": operation,
		"payload":   payload,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("forge: marshal request: %w", err)
	}

	token := c.mqtt.Publish(topic, 1, false, data)
	token.Wait()
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("forge: publish request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(c.cfg.Timeout)*time.Second)
	defer cancel()

	select {
	case resp := <-ch:
		if !resp.Success {
			return nil, fmt.Errorf("forge job failed: %s", resp.Error)
		}
		return &resp, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("forge: timeout waiting for %s response (correlation_id=%s)", operation, correlationID)
	}
}

// ollamaFallbackChat calls Ollama's /api/generate directly via the Ollama service.
// Used when FORGE_ENABLED=false.
func (c *Client) ollamaFallbackChat(prompt string) (string, error) {
	return c.ollama.GenerateCompletion(prompt)
}

// Disconnect cleanly closes the Forge MQTT connection.
func (c *Client) Disconnect() {
	if c.mqtt != nil {
		c.mqtt.Disconnect(250)
	}
}
