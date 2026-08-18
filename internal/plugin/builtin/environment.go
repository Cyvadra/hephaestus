package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/pkg/weather"
)

// WeatherClient retrieves the current weather for environment context.
type WeatherClient interface {
	Current(context.Context, weather.Location) (weather.Observation, error)
}

// EnvironmentConfig configures first-turn environment context.
type EnvironmentConfig struct {
	Location    string
	Coordinates weather.Location
	Timezone    string
	Weather     WeatherClient
	Now         func() time.Time
}

// EnvironmentPlugin persists basic time, location, and weather information
// before the first user message of each session.
type EnvironmentPlugin struct {
	config EnvironmentConfig
}

func NewEnvironmentPlugin(config EnvironmentConfig) *EnvironmentPlugin {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &EnvironmentPlugin{config: config}
}

func (p *EnvironmentPlugin) Name() string { return "environment" }

func (p *EnvironmentPlugin) Description() string {
	return "在首条消息中注入时间、地点和天气等基础环境信息。"
}

func (p *EnvironmentPlugin) Timeout() time.Duration { return 10 * time.Second }

func (p *EnvironmentPlugin) Handle(ctx context.Context, hook plugin.Hook, phase plugin.Phase, turn plugin.TurnContext) (plugin.TurnContext, error) {
	if hook != plugin.HookUserMessageIncoming || phase != plugin.PhaseAfter || !turn.IsFirstTurn || len(turn.Messages) == 0 {
		return turn, nil
	}
	location, err := time.LoadLocation(p.config.Timezone)
	if err != nil {
		return turn, fmt.Errorf("environment: load timezone: %w", err)
	}
	now := p.config.Now().In(location)
	return insertSystemMessage(turn, renderEnvironment(ctx, p.config, now), now), nil
}

func insertSystemMessage(turn plugin.TurnContext, content string, timestamp time.Time) plugin.TurnContext {
	last := len(turn.Messages) - 1
	message := store.ChatMessage{Role: ds4.RoleSystem, Content: content, Timestamp: timestamp}
	turn.Messages = append(turn.Messages, store.ChatMessage{})
	copy(turn.Messages[last+1:], turn.Messages[last:])
	turn.Messages[last] = message
	return turn
}

func renderEnvironment(ctx context.Context, config EnvironmentConfig, now time.Time) string {
	lines := []string{
		"[meta info begin]",
		fmt.Sprintf("Time: %s (%s)", now.Format("2006-01-02 15:04:05"), config.Timezone),
		fmt.Sprintf("Location: %s", config.Location),
	}
	if config.Weather != nil {
		if observation, err := config.Weather.Current(ctx, config.Coordinates); err == nil {
			lines = append(lines, fmt.Sprintf("Weather: %s, %.1fC, Humidity %d%%, Wind %.1f km/h", observation.Condition, observation.TemperatureC, observation.Humidity, observation.WindKPH))
		}
	}
	lines = append(lines, "[meta info end]")
	return strings.Join(lines, "\n")
}
