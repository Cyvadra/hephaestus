// Package weather retrieves current conditions from public weather providers.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	providerTimeout = 3 * time.Second
	cacheTTL        = 2 * time.Hour
)

// Location identifies a weather observation point.
type Location struct {
	Latitude  float64
	Longitude float64
}

// Observation is the normalized current weather returned by a Provider.
type Observation struct {
	Condition    string
	TemperatureC float64
	Humidity     int
	WindKPH      float64
	Provider     string
}

// Provider retrieves an observation for a coordinate.
type Provider interface {
	Name() string
	Current(context.Context, Location) (Observation, error)
}

// Client tries providers in order until one succeeds.
type Client struct {
	providers []Provider
	mu        sync.Mutex
	cache     map[Location]cachedObservation
}

type cachedObservation struct {
	observation Observation
	expiresAt   time.Time
}

// NewClient creates a fallback client from configured provider names.
func NewClient(httpClient *http.Client, names []string) (*Client, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	providers := make([]Provider, 0, len(names))
	for _, name := range names {
		switch name {
		case "open_meteo":
			providers = append(providers, OpenMeteoProvider{Client: httpClient})
		case "wttr":
			providers = append(providers, WttrProvider{Client: httpClient})
		case "met_no":
			providers = append(providers, METNorwayProvider{Client: httpClient})
		default:
			return nil, fmt.Errorf("weather: unknown provider %q", name)
		}
	}
	return NewClientWithProviders(providers), nil
}

// NewClientWithProviders creates a fallback client for custom providers.
func NewClientWithProviders(providers []Provider) *Client {
	return &Client{
		providers: append([]Provider(nil), providers...),
		cache:     make(map[Location]cachedObservation),
	}
}

// Current returns the first successful current observation.
func (c *Client) Current(ctx context.Context, location Location) (Observation, error) {
	if observation, ok := c.cached(location, time.Now()); ok {
		return observation, nil
	}

	var errs []error
	for _, provider := range c.providers {
		providerCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		observation, err := provider.Current(providerCtx, location)
		cancel()
		if err == nil {
			observation.Provider = provider.Name()
			c.storeCached(location, observation, time.Now())
			return observation, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", provider.Name(), err))
	}
	return Observation{}, fmt.Errorf("weather: all providers failed: %w", errorsJoin(errs))
}

func (c *Client) cached(location Location, now time.Time) (Observation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[location]
	if !ok || !now.Before(entry.expiresAt) {
		if ok {
			delete(c.cache, location)
		}
		return Observation{}, false
	}
	return entry.observation, true
}

func (c *Client) storeCached(location Location, observation Observation, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[location] = cachedObservation{observation: observation, expiresAt: now.Add(cacheTTL)}
}

// ProviderNames returns the provider names accepted by NewClient.
func ProviderNames() []string {
	return []string{"open_meteo", "wttr", "met_no"}
}

// OpenMeteoProvider uses the Open-Meteo forecast API.
type OpenMeteoProvider struct {
	Client  *http.Client
	BaseURL string
}

func (p OpenMeteoProvider) Name() string { return "open_meteo" }

func (p OpenMeteoProvider) Current(ctx context.Context, location Location) (Observation, error) {
	endpoint := p.BaseURL
	if endpoint == "" {
		endpoint = "https://api.open-meteo.com/v1/forecast"
	}
	query := url.Values{"latitude": {strconv.FormatFloat(location.Latitude, 'f', -1, 64)}, "longitude": {strconv.FormatFloat(location.Longitude, 'f', -1, 64)}, "current": {"temperature_2m,relative_humidity_2m,wind_speed_10m,weather_code"}}
	var response struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			Humidity    int     `json:"relative_humidity_2m"`
			WindSpeed   float64 `json:"wind_speed_10m"`
			WeatherCode int     `json:"weather_code"`
		} `json:"current"`
	}
	if err := getJSON(ctx, p.Client, endpoint+"?"+query.Encode(), nil, &response); err != nil {
		return Observation{}, err
	}
	return Observation{Condition: openMeteoCondition(response.Current.WeatherCode), TemperatureC: response.Current.Temperature, Humidity: response.Current.Humidity, WindKPH: response.Current.WindSpeed}, nil
}

// WttrProvider uses wttr.in's JSON endpoint.
type WttrProvider struct {
	Client  *http.Client
	BaseURL string
}

func (p WttrProvider) Name() string { return "wttr" }

func (p WttrProvider) Current(ctx context.Context, location Location) (Observation, error) {
	endpoint := p.BaseURL
	if endpoint == "" {
		endpoint = "https://wttr.in"
	}
	endpoint = strings.TrimRight(endpoint, "/") + "/" + url.PathEscape(fmt.Sprintf("%.4f,%.4f", location.Latitude, location.Longitude)) + "?format=j1"
	var response struct {
		Current []struct {
			Temperature string `json:"temp_C"`
			Humidity    string `json:"humidity"`
			WindKPH     string `json:"windspeedKmph"`
			Description []struct {
				Value string `json:"value"`
			} `json:"weatherDesc"`
		} `json:"current_condition"`
	}
	if err := getJSON(ctx, p.Client, endpoint, nil, &response); err != nil {
		return Observation{}, err
	}
	if len(response.Current) == 0 {
		return Observation{}, fmt.Errorf("missing current_condition")
	}
	current := response.Current[0]
	temperature, err := strconv.ParseFloat(current.Temperature, 64)
	if err != nil {
		return Observation{}, fmt.Errorf("parse temperature: %w", err)
	}
	humidity, err := strconv.Atoi(current.Humidity)
	if err != nil {
		return Observation{}, fmt.Errorf("parse humidity: %w", err)
	}
	wind, err := strconv.ParseFloat(current.WindKPH, 64)
	if err != nil {
		return Observation{}, fmt.Errorf("parse wind speed: %w", err)
	}
	condition := "未知"
	if len(current.Description) > 0 {
		condition = current.Description[0].Value
	}
	return Observation{Condition: condition, TemperatureC: temperature, Humidity: humidity, WindKPH: wind}, nil
}

// METNorwayProvider uses the MET Norway location forecast API.
type METNorwayProvider struct {
	Client  *http.Client
	BaseURL string
}

func (p METNorwayProvider) Name() string { return "met_no" }

func (p METNorwayProvider) Current(ctx context.Context, location Location) (Observation, error) {
	endpoint := p.BaseURL
	if endpoint == "" {
		endpoint = "https://api.met.no/weatherapi/locationforecast/2.0/compact"
	}
	query := url.Values{"lat": {strconv.FormatFloat(location.Latitude, 'f', -1, 64)}, "lon": {strconv.FormatFloat(location.Longitude, 'f', -1, 64)}}
	var response struct {
		Properties struct {
			Timeseries []struct {
				Data struct {
					Instant struct {
						Details struct {
							Temperature float64 `json:"air_temperature"`
							Humidity    float64 `json:"relative_humidity"`
							WindSpeed   float64 `json:"wind_speed"`
						} `json:"details"`
					} `json:"instant"`
					Next1Hours struct {
						Summary struct {
							SymbolCode string `json:"symbol_code"`
						} `json:"summary"`
					} `json:"next_1_hours"`
				} `json:"data"`
			} `json:"timeseries"`
		} `json:"properties"`
	}
	headers := http.Header{"User-Agent": {"Hephaestus/1.0 https://github.com/Cyvadra/hephaestus"}}
	if err := getJSON(ctx, p.Client, endpoint+"?"+query.Encode(), headers, &response); err != nil {
		return Observation{}, err
	}
	if len(response.Properties.Timeseries) == 0 {
		return Observation{}, fmt.Errorf("missing timeseries")
	}
	current := response.Properties.Timeseries[0].Data
	return Observation{Condition: strings.ReplaceAll(current.Next1Hours.Summary.SymbolCode, "_", " "), TemperatureC: current.Instant.Details.Temperature, Humidity: int(current.Instant.Details.Humidity), WindKPH: current.Instant.Details.WindSpeed * 3.6}, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, headers http.Header, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header = headers
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unexpected status %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func openMeteoCondition(code int) string {
	switch code {
	case 0:
		return "晴"
	case 1, 2:
		return "少云"
	case 3:
		return "阴"
	case 45, 48:
		return "雾"
	case 51, 53, 55, 56, 57:
		return "毛毛雨"
	case 61, 63, 65, 66, 67, 80, 81, 82:
		return "雨"
	case 71, 73, 75, 77, 85, 86:
		return "雪"
	case 95, 96, 99:
		return "雷暴"
	default:
		return "未知"
	}
}

func errorsJoin(errs []error) error {
	if len(errs) == 0 {
		return fmt.Errorf("no providers configured")
	}
	return fmt.Errorf("%s", strings.Join(errorStrings(errs), "; "))
}

func errorStrings(errs []error) []string {
	values := make([]string, len(errs))
	for index, err := range errs {
		values[index] = err.Error()
	}
	return values
}
