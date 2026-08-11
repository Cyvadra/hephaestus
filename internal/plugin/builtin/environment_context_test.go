package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestEnvironmentContextPluginInsertsAfterAttachmentPrefix(t *testing.T) {
	environmentPlugin := NewEnvironmentContextPlugin(EnvironmentContextConfig{
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
	content := got.Messages[0].Content
	if !strings.HasPrefix(content, attachment) || !strings.Contains(content, attachment+"[meta info begin]") || !strings.HasSuffix(content, "\n\n请总结") {
		t.Fatalf("unexpected message content: %q", content)
	}
}

func TestEnvironmentContextPluginWeatherFailureDoesNotBlockInsertion(t *testing.T) {
	environmentPlugin := NewEnvironmentContextPlugin(EnvironmentContextConfig{Location: "深圳", Timezone: "Asia/Shanghai", Weather: environmentWeatherStub{err: errors.New("down")}})
	turn := plugin.TurnContext{IsFirstTurn: true, Messages: []store.ChatMessage{{Role: "user", Content: "你好"}}}
	got, err := environmentPlugin.Handle(context.Background(), plugin.HookUserMessageIncoming, plugin.PhaseAfter, turn)
	if err != nil || !strings.Contains(got.Messages[0].Content, "[meta info begin]") || strings.Contains(got.Messages[0].Content, "Weather:") {
		t.Fatalf("unexpected degraded result: %q, %v", got.Messages[0].Content, err)
	}
}
