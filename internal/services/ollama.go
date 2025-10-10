package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/saaga/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type Ollama struct {
	config config.OllamaConfig
	client *http.Client
	logger *logrus.Logger
}

type OllamaRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Options struct {
		Temperature float64 `json:"temperature"`
		NumTokens   int     `json:"num_tokens,omitempty"`
	} `json:"options"`
	Stream bool `json:"stream"`
}

type OllamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

type ProcessedContent struct {
	Summary  string   `json:"summary"`
	Keywords []string `json:"keywords"`
	Insights string   `json:"insights"`
}

func NewOllama(cfg config.OllamaConfig) *Ollama {
	return &Ollama{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// ProcessContent sends content to Ollama for analysis and returns structured metadata
func (o *Ollama) ProcessContent(title, content string) (*ProcessedContent, error) {
	o.logger.WithFields(logrus.Fields{
		"title":          title,
		"content_length": len(content),
	}).Debug("Processing content with Ollama")

	// Create comprehensive prompt for analysis
	prompt := o.buildAnalysisPrompt(title, content)

	response, err := o.generateCompletion(prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate completion: %w", err)
	}

	// Parse the structured response
	processed, err := o.parseStructuredResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Ollama response: %w", err)
	}

	o.logger.WithFields(logrus.Fields{
		"keywords_count":  len(processed.Keywords),
		"summary_length":  len(processed.Summary),
		"insights_length": len(processed.Insights),
	}).Debug("Content processed successfully")

	return processed, nil
}

// buildAnalysisPrompt creates a comprehensive prompt for content analysis
func (o *Ollama) buildAnalysisPrompt(title, content string) string {
	return fmt.Sprintf(`Analyze the following article and provide a structured response in JSON format.

Title: %s

Content: %s

Please provide your analysis in the following JSON structure:
{
  "summary": "A concise 2-3 sentence summary of the main points",
  "keywords": ["keyword1", "keyword2", "keyword3", "keyword4", "keyword5"],
  "insights": "Key insights, implications, or notable aspects that would be useful for book recommendations"
}

Important:
- Keep the summary under 500 characters
- Provide exactly 5 relevant keywords that capture the essence of the content
- Focus insights on themes, concepts, or subjects that would help find related books
- Respond only with valid JSON, no additional text

JSON Response:`, title, content)
}

// generateCompletion sends a request to Ollama and returns the response
func (o *Ollama) generateCompletion(prompt string) (string, error) {
	reqBody := OllamaRequest{
		Model:  o.config.Model,
		Prompt: prompt,
		Stream: false,
	}
	reqBody.Options.Temperature = o.config.Temperature
	reqBody.Options.NumTokens = o.config.MaxTokens

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", o.config.BaseURL+"/api/generate", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var ollamaResp OllamaResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	return ollamaResp.Response, nil
}

// parseStructuredResponse attempts to parse the JSON response from Ollama
func (o *Ollama) parseStructuredResponse(response string) (*ProcessedContent, error) {
	// Clean up the response - sometimes LLMs add extra text
	response = o.extractJSON(response)

	var processed ProcessedContent
	if err := json.Unmarshal([]byte(response), &processed); err != nil {
		// Fallback: try to parse manually if JSON parsing fails
		return o.fallbackParsing(response)
	}

	// Validate and clean the parsed content
	if processed.Summary == "" {
		processed.Summary = "No summary available"
	}
	if len(processed.Keywords) == 0 {
		processed.Keywords = []string{"general", "article", "content"}
	}
	if processed.Insights == "" {
		processed.Insights = "No specific insights identified"
	}

	return &processed, nil
}

// extractJSON attempts to extract JSON from the response
func (o *Ollama) extractJSON(response string) string {
	// Find JSON object in the response
	start := -1
	end := -1
	braceCount := 0

	for i, char := range response {
		if char == '{' {
			if start == -1 {
				start = i
			}
			braceCount++
		} else if char == '}' {
			braceCount--
			if braceCount == 0 && start != -1 {
				end = i + 1
				break
			}
		}
	}

	if start != -1 && end != -1 {
		return response[start:end]
	}

	return response
}

// fallbackParsing provides a fallback when JSON parsing fails
func (o *Ollama) fallbackParsing(response string) (*ProcessedContent, error) {
	o.logger.Warn("JSON parsing failed, using fallback parsing")

	// Simple fallback - treat the entire response as insights
	return &ProcessedContent{
		Summary:  "Summary not available due to parsing error",
		Keywords: []string{"parsing", "error", "fallback", "content", "analysis"},
		Insights: response,
	}, nil
}

// SetLogger sets the logger instance
func (o *Ollama) SetLogger(logger *logrus.Logger) {
	o.logger = logger
}
