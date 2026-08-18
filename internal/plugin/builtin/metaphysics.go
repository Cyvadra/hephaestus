package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/pkg/lunar"
	"github.com/Cyvadra/hephaestus/pkg/qimen"
)

// MetaphysicsConfig configures first-turn traditional Chinese metaphysics context.
type MetaphysicsConfig struct {
	Timezone string
	Now      func() time.Time
}

// MetaphysicsPlugin persists lunar calendar and Qimen data before the first
// user message of each session.
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
	return "在首条消息中注入黄历、四柱和奇门遁甲等玄学信息。"
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
	return insertSystemMessage(turn, renderMetaphysics(now), now), nil
}

func renderMetaphysics(now time.Time) string {
	lunarDate := lunar.FromTime(now)
	lines := []string{
		"[metaphysics info begin]",
		fmt.Sprintf("农历: %s", lunarDate.Date),
		fmt.Sprintf("四柱: %s %s %s %s", lunarDate.Year, lunarDate.Month, lunarDate.Day, lunarDate.Hour),
		qimen.Render(now),
		"<notice>奇门遁甲排盘已完整提供。后续仅可依据以上盘面象数进行解读；不得重新计算历法、推演排盘，或以任何方式补充、改写盘面数据。</notice>",
		"[metaphysics info end]",
	}
	return strings.Join(lines, "\n")
}
