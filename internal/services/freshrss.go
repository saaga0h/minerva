package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saaga/minerva/internal/config"
	"github.com/sirupsen/logrus"
)

type FreshRSS struct {
	config config.FreshRSSConfig
	client *http.Client
	logger *logrus.Logger
}

type FreshRSSItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Link    string `json:"link"`
	Content string `json:"content"`
	PubDate string `json:"pubDate"`
	Starred bool   `json:"starred"`
}

type FreshRSSResponse struct {
	Items []FreshRSSItem `json:"items"`
}

// Fever API response structures
type FeverResponse struct {
	Auth  int         `json:"auth"`
	Items []FeverItem `json:"items"`
}

type FeverSavedResponse struct {
	Auth                int    `json:"auth"`
	APIVersion          int    `json:"api_version"`
	LastRefreshedOnTime int64  `json:"last_refreshed_on_time"`
	SavedItemIDs        string `json:"saved_item_ids"`
}

type FeverItem struct {
	ID            interface{} `json:"id"`      // Can be string or number
	FeedID        interface{} `json:"feed_id"` // Can be string or number
	Title         string      `json:"title"`
	Author        string      `json:"author"`
	HTML          string      `json:"html"`
	URL           string      `json:"url"`
	IsSaved       interface{} `json:"is_saved"`        // Can be string or number
	IsRead        interface{} `json:"is_read"`         // Can be string or number
	CreatedOnTime interface{} `json:"created_on_time"` // Can be string or number
}

func NewFreshRSS(cfg config.FreshRSSConfig) *FreshRSS {
	return &FreshRSS{
		config: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
		logger: logrus.New(),
	}
}

// GetStarredItems fetches starred/favorite RSS items from FreshRSS using Fever API
func (f *FreshRSS) GetStarredItems() ([]FreshRSSItem, error) {
	f.logger.WithField("url", f.config.BaseURL).Debug("Fetching starred items from FreshRSS using Fever API")

	// First, get saved item IDs
	savedIDs, err := f.getSavedItemIDs()
	if err != nil {
		return nil, fmt.Errorf("failed to get saved item IDs: %w", err)
	}

	if len(savedIDs) == 0 {
		f.logger.Info("No saved items found")
		return []FreshRSSItem{}, nil
	}

	f.logger.WithField("saved_ids_count", len(savedIDs)).Debug("Getting items by IDs")

	// Then get the actual items using the with_ids parameter for efficiency
	items, err := f.getItemsByIDs(savedIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get items by IDs: %w", err)
	}

	f.logger.WithField("count", len(items)).Info("Fetched starred items from FreshRSS")
	return items, nil
}

// getSavedItemIDs fetches the list of saved item IDs
func (f *FreshRSS) getSavedItemIDs() ([]string, error) {
	f.logger.Debug("=== getSavedItemIDs function called ===")

	// Create the URL with saved_item_ids parameter
	apiURL := f.config.BaseURL + "&saved_item_ids"
	f.logger.WithField("api_url", apiURL).Debug("Making request to saved_item_ids endpoint")

	// Create form data
	data := url.Values{}
	data.Set("api_key", f.config.APIKey)

	resp, err := http.PostForm(apiURL, data)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	responseLen := len(body)
	if responseLen > 200 {
		responseLen = 200
	}
	f.logger.WithField("response", string(body)[:responseLen]).Debug("Fever API response for saved IDs")

	var response FeverSavedResponse
	if err := json.Unmarshal(body, &response); err != nil {
		f.logger.WithError(err).WithField("body", string(body)).Error("Failed to unmarshal saved IDs response")
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	f.logger.WithFields(logrus.Fields{
		"auth":                   response.Auth,
		"api_version":            response.APIVersion,
		"last_refreshed_on_time": response.LastRefreshedOnTime,
		"saved_item_ids_raw":     response.SavedItemIDs,
	}).Debug("Unmarshaled saved IDs response")

	if response.Auth != 1 {
		return nil, fmt.Errorf("authentication failed with Fever API")
	}

	// Parse saved item IDs from comma-separated string
	var savedIDs []string
	if response.SavedItemIDs != "" {
		savedIDs = strings.Split(response.SavedItemIDs, ",")
	}

	f.logger.WithFields(logrus.Fields{
		"saved_item_ids_string": response.SavedItemIDs,
		"parsed_ids_count":      len(savedIDs),
		"parsed_ids":            savedIDs,
	}).Debug("Parsed saved item IDs")

	return savedIDs, nil
}

// getItemsByIDs fetches specific items by their IDs using the with_ids parameter
func (f *FreshRSS) getItemsByIDs(ids []string) ([]FreshRSSItem, error) {
	if len(ids) == 0 {
		return []FreshRSSItem{}, nil
	}

	f.logger.WithFields(logrus.Fields{
		"saved_ids_to_find": ids,
		"request_ids_count": len(ids),
	}).Debug("Making API request to find saved items using with_ids parameter")

	// The Fever API supports up to 50 items with with_ids parameter
	// Join the IDs with commas
	idsParam := strings.Join(ids, ",")

	// Create the URL with items parameter and with_ids
	apiURL := f.config.BaseURL + "&items&with_ids=" + idsParam
	f.logger.WithField("api_url", apiURL).WithField("with_ids", idsParam).Debug("Making request to items endpoint with with_ids")

	// Create form data
	data := url.Values{}
	data.Set("api_key", f.config.APIKey)

	resp, err := http.PostForm(apiURL, data)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	responseLen := len(body)
	if responseLen > 200 {
		responseLen = 200
	}
	f.logger.WithField("response", string(body)[:responseLen]).Debug("Fever API response for items with with_ids")

	var response FeverResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Auth != 1 {
		return nil, fmt.Errorf("authentication failed with Fever API")
	}

	// Convert Fever items to our format
	var items []FreshRSSItem
	itemIDsFound := make([]string, 0)

	for _, item := range response.Items {
		itemIDStr := f.interfaceToString(item.ID)
		itemIDsFound = append(itemIDsFound, itemIDStr)

		// Convert to our format
		rssItem := FreshRSSItem{
			ID:      itemIDStr,
			Title:   item.Title,
			Link:    item.URL,
			Content: item.HTML,
			PubDate: f.interfaceToString(item.CreatedOnTime),
			Starred: true, // These are saved items requested by ID, so they're starred
		}
		items = append(items, rssItem)
		f.logger.WithField("found_saved_item_id", itemIDStr).WithField("title", item.Title).Debug("Found saved item")
	}

	f.logger.WithFields(logrus.Fields{
		"total_items_in_response": len(response.Items),
		"found_saved_items":       len(items),
		"requested_ids_count":     len(ids),
		"found_item_ids":          itemIDsFound,
	}).Debug("Retrieved specific items by IDs")

	return items, nil
}

// SetLogger sets the logger instance
func (f *FreshRSS) SetLogger(logger *logrus.Logger) {
	f.logger = logger
}

// interfaceToString safely converts interface{} to string, handling both strings and numbers
func (f *FreshRSS) interfaceToString(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case float64:
		return fmt.Sprintf("%.0f", v)
	case float32:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}
