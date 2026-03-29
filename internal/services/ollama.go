package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/saaga0h/minerva/internal/config"
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

// Multi-pass results
type MultiPassResult struct {
	Pass1 ClassificationResult `json:"pass1"`
	Pass2 EntityResult         `json:"pass2"`
	Pass3 ConceptResult        `json:"pass3"`
}

type ClassificationResult struct {
	Domain string `json:"domain"`
	Type   string `json:"type"`
	Topic  string `json:"topic"`
}

type EntityResult struct {
	Facilities []string `json:"facilities"`
	People     []string `json:"people"`
	Locations  []string `json:"locations"`
	Phenomena  []string `json:"phenomena"`
}

type ConceptResult struct {
	Concepts      []string `json:"concepts"`
	RelatedTopics []string `json:"related_topics"`
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

// ProcessContentMultiPass performs multi-pass analysis with optional debugging
func (o *Ollama) ProcessContentMultiPass(title, content string, articleID int, debug bool) (*MultiPassResult, error) {
	o.logger.WithFields(logrus.Fields{
		"article_id": articleID,
		"title":      title,
		"debug":      debug,
	}).Info("Starting multi-pass content analysis")

	if debug {
		if err := os.MkdirAll("./debug", 0755); err != nil {
			o.logger.WithError(err).Warn("Failed to create debug directory")
		}
	}

	result := &MultiPassResult{}

	// Pass 1: Classify the article
	o.logger.Debug("Pass 1: Classifying article")
	pass1, err := o.classifyArticle(title, content, articleID, debug)
	if err != nil {
		return nil, fmt.Errorf("pass 1 failed: %w", err)
	}
	result.Pass1 = pass1

	// Pass 2: Extract entities (using Pass 1 context)
	o.logger.WithField("domain", pass1.Domain).Debug("Pass 2: Extracting entities")
	pass2, err := o.extractEntities(title, content, pass1, articleID, debug)
	if err != nil {
		return nil, fmt.Errorf("pass 2 failed: %w", err)
	}
	result.Pass2 = pass2

	// Pass 3: Extract concepts (using Pass 1 + 2 context)
	o.logger.Debug("Pass 3: Extracting concepts")
	pass3, err := o.extractConcepts(title, content, pass1, pass2, articleID, debug)
	if err != nil {
		return nil, fmt.Errorf("pass 3 failed: %w", err)
	}
	result.Pass3 = pass3

	o.logger.WithFields(logrus.Fields{
		"article_id":     articleID,
		"domain":         pass1.Domain,
		"entities":       len(pass2.Facilities) + len(pass2.People) + len(pass2.Locations) + len(pass2.Phenomena),
		"concepts":       len(pass3.Concepts),
		"related_topics": len(pass3.RelatedTopics),
	}).Info("Multi-pass analysis completed")

	if debug {
		// Save combined result
		prettyJSON, _ := json.MarshalIndent(result, "", "  ")
		os.WriteFile(fmt.Sprintf("./debug/article-%d-complete.json", articleID), prettyJSON, 0644)
	}

	return result, nil
}

// classifyArticle - Pass 1: Domain and topic classification
func (o *Ollama) classifyArticle(title, content string, articleID int, debug bool) (ClassificationResult, error) {
	prompt := fmt.Sprintf(`Classify this article. Output ONLY valid JSON, no other text.

Title: %s

Content: %s

Output JSON format:
{
  "domain": "physics|climate|programming|medicine|biology|astronomy|other",
  "type": "discovery|review|tutorial|opinion|news",
  "topic": "brief one-sentence summary"
}

JSON:`, title, content)

	if debug {
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass1-prompt.txt", articleID), []byte(prompt), 0644)
	}

	response, err := o.generateCompletion(prompt)
	if err != nil {
		return ClassificationResult{}, err
	}

	if debug {
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass1-response.txt", articleID), []byte(response), 0644)
	}

	var result ClassificationResult
	cleanJSON := o.extractJSON(response)

	if err := json.Unmarshal([]byte(cleanJSON), &result); err != nil {
		if debug {
			os.WriteFile(fmt.Sprintf("./debug/article-%d-pass1-ERROR.txt", articleID),
				[]byte(fmt.Sprintf("Parse error: %s\n\nClean JSON attempted:\n%s", err.Error(), cleanJSON)), 0644)
		}
		return ClassificationResult{}, fmt.Errorf("failed to parse classification: %w", err)
	}

	if debug {
		prettyJSON, _ := json.MarshalIndent(result, "", "  ")
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass1-parsed.json", articleID), prettyJSON, 0644)
	}

	o.logger.WithFields(logrus.Fields{
		"article_id": articleID,
		"domain":     result.Domain,
		"type":       result.Type,
	}).Debug("Pass 1: Classification completed")

	return result, nil
}

// extractEntities - Pass 2: Named entity extraction based on domain
func (o *Ollama) extractEntities(title, content string, classification ClassificationResult, articleID int, debug bool) (EntityResult, error) {
	prompt := fmt.Sprintf(`This is a %s article about: %s

Title: %s

Content: %s

Extract named entities from this article. Output ONLY valid JSON, no other text.

For %s domain, focus on:
- Facilities/Instruments (labs, detectors, telescopes, tools)
- People/Organizations (researchers, institutions, companies)
- Locations (specific places, regions)
- Phenomena (specific events, processes, discoveries)

Output JSON format:
{
  "facilities": ["entity1", "entity2"],
  "people": ["person1", "organization1"],
  "locations": ["location1"],
  "phenomena": ["phenomenon1", "phenomenon2"]
}

JSON:`, classification.Domain, classification.Topic, title, content, classification.Domain)

	if debug {
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass2-prompt.txt", articleID), []byte(prompt), 0644)
	}

	response, err := o.generateCompletion(prompt)
	if err != nil {
		return EntityResult{}, err
	}

	if debug {
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass2-response.txt", articleID), []byte(response), 0644)
	}

	var result EntityResult
	cleanJSON := o.extractJSON(response)

	if err := json.Unmarshal([]byte(cleanJSON), &result); err != nil {
		if debug {
			os.WriteFile(fmt.Sprintf("./debug/article-%d-pass2-ERROR.txt", articleID),
				[]byte(fmt.Sprintf("Parse error: %s\n\nClean JSON attempted:\n%s", err.Error(), cleanJSON)), 0644)
		}
		return EntityResult{}, fmt.Errorf("failed to parse entities: %w", err)
	}

	if debug {
		prettyJSON, _ := json.MarshalIndent(result, "", "  ")
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass2-parsed.json", articleID), prettyJSON, 0644)
	}

	o.logger.WithFields(logrus.Fields{
		"article_id": articleID,
		"facilities": len(result.Facilities),
		"people":     len(result.People),
		"locations":  len(result.Locations),
		"phenomena":  len(result.Phenomena),
	}).Debug("Pass 2: Entity extraction completed")

	return result, nil
}

// extractConcepts - Pass 3: Conceptual understanding and related topics
func (o *Ollama) extractConcepts(title, content string, classification ClassificationResult, entities EntityResult, articleID int, debug bool) (ConceptResult, error) {
	prompt := fmt.Sprintf(`This is a %s article about: %s

We've identified these entities:
- Facilities: %v
- People: %v
- Locations: %v
- Phenomena: %v

Title: %s

Content: %s

What conceptual knowledge would help understand this article deeply?
What related topics would provide context?

Output ONLY valid JSON, no other text.

Output JSON format:
{
  "concepts": ["concept1", "concept2", "concept3"],
  "related_topics": ["topic1", "topic2", "topic3"]
}

Focus on:
- Fundamental theories or principles
- Methods or techniques
- Related fields of study
- Prerequisites for understanding

JSON:`, classification.Domain, classification.Topic,
		entities.Facilities, entities.People, entities.Locations, entities.Phenomena,
		title, content)

	if debug {
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass3-prompt.txt", articleID), []byte(prompt), 0644)
	}

	response, err := o.generateCompletion(prompt)
	if err != nil {
		return ConceptResult{}, err
	}

	if debug {
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass3-response.txt", articleID), []byte(response), 0644)
	}

	var result ConceptResult
	cleanJSON := o.extractJSON(response)

	if err := json.Unmarshal([]byte(cleanJSON), &result); err != nil {
		if debug {
			os.WriteFile(fmt.Sprintf("./debug/article-%d-pass3-ERROR.txt", articleID),
				[]byte(fmt.Sprintf("Parse error: %s\n\nClean JSON attempted:\n%s", err.Error(), cleanJSON)), 0644)
		}
		return ConceptResult{}, fmt.Errorf("failed to parse concepts: %w", err)
	}

	if debug {
		prettyJSON, _ := json.MarshalIndent(result, "", "  ")
		os.WriteFile(fmt.Sprintf("./debug/article-%d-pass3-parsed.json", articleID), prettyJSON, 0644)
	}

	o.logger.WithFields(logrus.Fields{
		"article_id":     articleID,
		"concepts":       len(result.Concepts),
		"related_topics": len(result.RelatedTopics),
	}).Debug("Pass 3: Concept extraction completed")

	return result, nil
}

// buildAnalysisPrompt creates a comprehensive prompt for content analysis (legacy)
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

// parseStructuredResponse attempts to parse the JSON response from Ollama (legacy)
func (o *Ollama) parseStructuredResponse(response string) (*ProcessedContent, error) {
	response = o.extractJSON(response)

	var processed ProcessedContent
	if err := json.Unmarshal([]byte(response), &processed); err != nil {
		return o.fallbackParsing(response)
	}

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
		extracted := response[start:end]

		// Fix common JSON escaping issues from LLM output
		extracted = strings.ReplaceAll(extracted, `\_`, `_`)
		extracted = strings.ReplaceAll(extracted, `\-`, `-`)

		// Remove any backslash before alphanumeric characters (LLM over-escaping)
		re := regexp.MustCompile(`\\([a-zA-Z0-9])`)
		extracted = re.ReplaceAllString(extracted, `$1`)

		return extracted
	}

	// Fallback: If no braces found, check if response looks like JSON content without wrapper
	trimmed := strings.TrimSpace(response)
	if strings.HasPrefix(trimmed, `"`) && strings.Contains(trimmed, `":`) {
		// Looks like JSON fields without braces - wrap it
		o.logger.Warn("Response missing JSON braces, attempting to wrap")
		wrapped := "{" + trimmed

		// Check if it ends with }
		if !strings.HasSuffix(wrapped, "}") {
			wrapped += "}"
		}

		// Clean up escaping
		wrapped = strings.ReplaceAll(wrapped, `\_`, `_`)
		wrapped = strings.ReplaceAll(wrapped, `\-`, `-`)
		re := regexp.MustCompile(`\\([a-zA-Z0-9])`)
		wrapped = re.ReplaceAllString(wrapped, `$1`)

		return wrapped
	}

	return response
}

// fallbackParsing provides a fallback when JSON parsing fails (legacy)
func (o *Ollama) fallbackParsing(response string) (*ProcessedContent, error) {
	o.logger.Warn("JSON parsing failed, using fallback parsing")

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

// OllamaEmbedRequest is the request body for the Ollama /api/embed endpoint.
type OllamaEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// OllamaEmbedResponse is the response from the Ollama /api/embed endpoint.
type OllamaEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns a semantic embedding vector for the given text using the configured
// embed model. Unlike chat calls it does NOT acquire any mutex — embedding is
// stateless and concurrent-safe.
func (o *Ollama) Embed(text string) ([]float32, error) {
	reqBody := OllamaEmbedRequest{
		Model: o.config.EmbedModel,
		Input: text,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}

	req, err := http.NewRequest("POST", o.config.BaseURL+"/api/embed", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed unexpected status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embed response: %w", err)
	}

	var embedResp OllamaEmbedResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return nil, fmt.Errorf("failed to parse embed response: %w", err)
	}

	if len(embedResp.Embeddings) == 0 {
		return nil, fmt.Errorf("embed response contains no embeddings")
	}

	return embedResp.Embeddings[0], nil
}
