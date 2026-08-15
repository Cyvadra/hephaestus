package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Cyvadra/ds4"
	"github.com/Cyvadra/hephaestus/internal/plugin"
	"github.com/Cyvadra/hephaestus/internal/store"
	"github.com/Cyvadra/hephaestus/pkg/weather"
)

type environmentWeatherStub struct {
	observation weather.Observation
	err         error
}

func (s environmentWeatherStub) Current(context.Context, weather.Location) (weather.Observation, error) {
	return s.observation, s.err
}

func TestMetaphysicsPluginInsertsSystemMessageBeforeUserMessage(t *testing.T) {
	environmentPlugin := NewMetaphysicsPlugin(MetaphysicsConfig{
		Location: "深圳", Timezone: "Asia/Shanghai",
		Weather: environmentWeatherStub{observation: weather.Observation{Condition: "晴", TemperatureC: 30, Humidity: 80, WindKPH: 10}},
		Now:     func() time.Time { return time.Date(2024, time.February, 10, 12, 0, 0, 0, time.UTC) },
	})
	attachment := "[file name]: uploads/a.txt\n[file size]: 1.0 KB\n[file content begin]\nfirst\n\nsecond\n[file content end]\n\n"
	turn := plugin.TurnContext{IsFirstTurn: true, Messages: []store.ChatMessage{{Role: "user", Content: attachment + "请总结"}}}

	got, err := environmentPlugin.Handle(context.Background(), plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(got.Messages))
	}
	if got.Messages[0].Role != ds4.RoleSystem || !strings.Contains(got.Messages[0].Content, "[meta info begin]") {
		t.Fatalf("unexpected environment message: %#v", got.Messages[0])
	}
	if !strings.Contains(got.Messages[0].Content, "[奇门遁甲 snapshot begin]") {
		t.Fatalf("environment message does not include Qimen chart: %q", got.Messages[0].Content)
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != attachment+"请总结" {
		t.Fatalf("unexpected user message: %#v", got.Messages[1])
	}
}

func TestMetaphysicsPluginWeatherFailureDoesNotBlockInsertion(t *testing.T) {
	environmentPlugin := NewMetaphysicsPlugin(MetaphysicsConfig{Location: "深圳", Timezone: "Asia/Shanghai", Weather: environmentWeatherStub{err: errors.New("down")}})
	turn := plugin.TurnContext{IsFirstTurn: true, Messages: []store.ChatMessage{{Role: "user", Content: "你好"}}}
	got, err := environmentPlugin.Handle(context.Background(), plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	if err != nil || len(got.Messages) != 2 || !strings.Contains(got.Messages[0].Content, "[meta info begin]") || strings.Contains(got.Messages[0].Content, "Weather:") || got.Messages[1].Content != "你好" {
		t.Fatalf("unexpected degraded result: %#v, %v", got.Messages, err)
	}
}
