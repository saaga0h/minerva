package mqtt

import (
	"encoding/json"
	"fmt"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/sirupsen/logrus"
)

const (
	qos           = 1
	connectTimeout = 10 * time.Second
)

// ClientConfig holds MQTT connection settings.
type ClientConfig struct {
	BrokerURL string // e.g. "tcp://localhost:1883"
	ClientID  string // unique per primitive, e.g. "minerva-extractor"
	Username  string // optional, required for brokers with auth
	Password  string // optional
}

// Client wraps the paho MQTT client with structured logging and JSON marshaling.
type Client struct {
	config ClientConfig
	client paho.Client
	logger *logrus.Logger
}

// NewClient creates and connects an MQTT client.
func NewClient(cfg ClientConfig) (*Client, error) {
	c := &Client{
		config: cfg,
		logger: logrus.New(),
	}

	opts := paho.NewClientOptions().
		AddBroker(cfg.BrokerURL).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetOnConnectHandler(func(_ paho.Client) {
			c.logger.WithField("broker", cfg.BrokerURL).Info("MQTT connected")
		}).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			c.logger.WithError(err).Warn("MQTT connection lost, reconnecting")
		})

	client := paho.NewClient(opts)

	token := client.Connect()
	if !token.WaitTimeout(connectTimeout) {
		return nil, fmt.Errorf("MQTT connect timeout after %s", connectTimeout)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("MQTT connect failed: %w", err)
	}

	c.client = client
	return c, nil
}

// SetLogger replaces the default logger.
func (c *Client) SetLogger(logger *logrus.Logger) {
	c.logger = logger
}

// Publish marshals payload to JSON and publishes to the given topic at QoS 1.
func (c *Client) Publish(topic string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	token := c.client.Publish(topic, qos, false, data)
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("failed to publish to %s: %w", topic, err)
	}

	c.logger.WithFields(logrus.Fields{
		"topic": topic,
		"bytes": len(data),
	}).Debug("Published message")

	return nil
}

// Subscribe registers a handler for the given topic at QoS 1.
// The handler receives the raw JSON payload bytes.
func (c *Client) Subscribe(topic string, handler func(payload []byte)) error {
	token := c.client.Subscribe(topic, qos, func(_ paho.Client, msg paho.Message) {
		c.logger.WithFields(logrus.Fields{
			"topic": msg.Topic(),
			"bytes": len(msg.Payload()),
		}).Debug("Received message")
		handler(msg.Payload())
	})
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("failed to subscribe to %s: %w", topic, err)
	}

	c.logger.WithField("topic", topic).Info("Subscribed to topic")
	return nil
}

// PublishRaw publishes a pre-serialized JSON payload to a topic at QoS 1.
func (c *Client) PublishRaw(topic string, payload []byte) error {
	token := c.client.Publish(topic, qos, false, payload)
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("failed to publish to %s: %w", topic, err)
	}

	c.logger.WithFields(logrus.Fields{
		"topic": topic,
		"bytes": len(payload),
	}).Debug("Published raw message")

	return nil
}

// Disconnect cleanly closes the MQTT connection.
func (c *Client) Disconnect() {
	c.client.Disconnect(250)
	c.logger.Info("MQTT disconnected")
}
