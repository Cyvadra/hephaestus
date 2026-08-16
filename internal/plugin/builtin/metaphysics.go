package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/pkg/lunar"
	"github.com/Cyvadra/hephaestus/pkg/qimen"
	"github.com/Cyvadra/hephaestus/pkg/weather"
)

// WeatherClient retrieves the current weather for environment context.
type WeatherClient interface {
	Current(context.Context, weather.Location) (weather.Observation, error)
}

// MetaphysicsConfig configures first-turn environment context.
type MetaphysicsConfig struct {
	Location    string
	Coordinates weather.Location
	Timezone    string
	Weather     WeatherClient
	Now         func() time.Time
}

// MetaphysicsPlugin persists time, lunar calendar, and weather before
// the first user message of each session.
type MetaphysicsPlugin struct {
	config MetaphysicsConfig
}

func NewMetaphysicsPlugin(config MetaphysicsConfig) *MetaphysicsPlugin {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &MetaphysicsPlugin{config: config}
}

func (p *MetaphysicsPlugin) Name() string { return "metaphysics" }

func (p *MetaphysicsPlugin) Description() string {
	return "在首条消息中注入时间、地点、天气和农历环境信息。"
}

func (p *MetaphysicsPlugin) Timeout() time.Duration { return 10 * time.Second }

func (p *MetaphysicsPlugin) Handle(ctx context.Context, hook plugin.Hook, phase plugin.Phase, turn plugin.TurnContext) (plugin.TurnContext, error) {
	if hook != plugin.HookUserMessageIncoming || phase != plugin.PhaseAfter || !turn.IsFirstTurn || len(turn.Messages) == 0 {
		return turn, nil
	}
	location, err := time.LoadLocation(p.config.Timezone)
	if err != nil {
		return turn, fmt.Errorf("metaphysics: load timezone: %w", err)
	}
	now := p.config.Now().In(location)
	content := renderMetaphysics(ctx, p.config, now)
	last := len(turn.Messages) - 1
	environmentMessage := store.ChatMessage{Role: ds4.RoleSystem, Content: content, Timestamp: now}
	turn.Messages = append(turn.Messages, store.ChatMessage{})
	copy(turn.Messages[last+1:], turn.Messages[last:])
	turn.Messages[last] = environmentMessage
	return turn, nil
}

func renderMetaphysics(ctx context.Context, config MetaphysicsConfig, now time.Time) string {
	lunarDate := lunar.FromTime(now)
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
	lines = append(lines,
		fmt.Sprintf("农历: %s", lunarDate.Date),
		fmt.Sprintf("四柱: %s %s %s %s", lunarDate.Year, lunarDate.Month, lunarDate.Day, lunarDate.Hour),
		qimen.Render(now),
		"<notice>奇门遁甲排盘已完整提供。后续仅可依据以上盘面象数进行解读；不得重新计算历法、推演排盘，或以任何方式补充、改写盘面数据。</notice>",
		"[meta info end]",
	)
	return strings.Join(lines, "\n")
}
