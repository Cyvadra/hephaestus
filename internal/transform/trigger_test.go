package transform

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyvadra/hephaestus/internal/llm"
	"github.com/Cyvadra/hephaestus/internal/registry"
)

func triggerReply(w http.ResponseWriter, content string) {
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"message":       map[string]any{"role": "assistant", "content": content},
		"finish_reason": "stop",
	}}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func TestGenerateTriggerParsesFencedJSON(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		triggerReply(w, "```json\n{\"expression\":\"Hour >= 9 && ExecutionsToday == 0\",\"explanation\":\"每天9点\"}\n```")
	})
	got, err := GenerateTrigger(context.Background(), client, "每天9点", "")
	if err != nil {
		t.Fatalf("GenerateTrigger() error = %v", err)
	}
	if got.Expression != "Hour >= 9 && ExecutionsToday == 0" || got.Explanation != "每天9点" {
		t.Fatalf("GenerateTrigger() = %+v", got)
	}
}

func TestGenerateTriggerRetriesOnCompileError(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			triggerReply(w, `{"expression":"hour >= 9","explanation":"x"}`)
			return
		}
		triggerReply(w, `{"expression":"Hour >= 9 && ExecutionsToday == 0","explanation":"x"}`)
	})
	got, err := GenerateTrigger(context.Background(), client, "9点", "")
	if err != nil {
		t.Fatalf("GenerateTrigger() error = %v", err)
	}
	if calls.Load() != 2 || got.Expression != "Hour >= 9 && ExecutionsToday == 0" {
		t.Fatalf("calls=%d got=%+v", calls.Load(), got)
	}
}

func TestGenerateTriggerFailsAfterRetry(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		triggerReply(w, `{"expression":"Hour + 1","explanation":"x"}`)
	})
	if _, err := GenerateTrigger(context.Background(), client, "9点", ""); err == nil {
		t.Fatal("GenerateTrigger() error = nil, want compile error")
	}
}

func TestGenerateTriggerRejectsEmptyDescription(t *testing.T) {
	if _, err := GenerateTrigger(context.Background(), nil, "  ", ""); err != ErrTriggerDescriptionEmpty {
		t.Fatalf("err = %v", err)
	}
}

// triggerCase checks a generated expression against sample environments.
type triggerCase struct {
	description string
	checks      []triggerCheck
}

type triggerCheck struct {
	name string
	env  registry.TriggerEnv
	want bool
}

// liveEnv builds an env at 2026-11-09 (a Monday) + dayOffset, hh:mm.
func liveEnv(dayOffset, hour, minute, executionsToday int) registry.TriggerEnv {
	now := time.Date(2026, 11, 9+dayOffset, hour, minute, 0, 0, time.Local)
	env := registry.TriggerEnv{
		Now: now, Date: now.Format("2006-01-02"), Hour: hour, Minute: minute,
		Weekday: int(now.Weekday()), ExecutionsToday: executionsToday,
		HasMessages: true, LastMessageAt: now.Add(-time.Minute), IdleSeconds: 60,
	}
	if executionsToday > 0 {
		env.HasLastStarted, env.LastStartedAt = true, now.Add(-10*time.Minute)
		env.HasLastSucceeded, env.LastSucceededAt = true, now.Add(-5*time.Minute)
		env.LastSucceededDate = env.Date
	} else {
		env.HasLastStarted, env.LastStartedAt = true, now.Add(-24*time.Hour)
		env.HasLastSucceeded, env.LastSucceededAt = true, now.Add(-24*time.Hour)
		env.LastSucceededDate = env.LastSucceededAt.Format("2006-01-02")
	}
	return env
}

func withIdle(env registry.TriggerEnv, idle time.Duration, startedSince bool) registry.TriggerEnv {
	env.LastMessageAt = env.Now.Add(-idle)
	env.IdleSeconds = idle.Seconds()
	if startedSince {
		env.LastStartedAt = env.LastMessageAt.Add(time.Minute)
	} else {
		env.LastStartedAt = env.LastMessageAt.Add(-time.Hour)
	}
	return env
}

func withLastStarted(env registry.TriggerEnv, ago time.Duration) registry.TriggerEnv {
	env.HasLastStarted, env.LastStartedAt = true, env.Now.Add(-ago)
	return env
}

// TestGenerateTriggerLive exercises the real prompt against DeepSeek:
// HEPHAESTUS_RUN_LIVE_TESTS=1 go test ./internal/transform -run TriggerLive -v
func TestGenerateTriggerLive(t *testing.T) {
	key := os.Getenv("HEPHAESTUS_DEEPSEEK_API_KEY")
	if os.Getenv("HEPHAESTUS_RUN_LIVE_TESTS") == "" || key == "" {
		t.Skip("set HEPHAESTUS_RUN_LIVE_TESTS=1 and HEPHAESTUS_DEEPSEEK_API_KEY")
	}
	client := llm.New(key)
	cases := []triggerCase{
		{"每天早上9点", []triggerCheck{
			{"8:59", liveEnv(0, 8, 59, 0), false}, {"9:00", liveEnv(0, 9, 0, 0), true},
			{"9:01 after run", liveEnv(0, 9, 1, 1), false}, {"sunday 9", liveEnv(6, 9, 0, 0), true},
		}},
		{"工作日早上9点跑一次", []triggerCheck{
			{"mon 9", liveEnv(0, 9, 0, 0), true}, {"sat 9", liveEnv(5, 9, 0, 0), false},
			{"sun 10", liveEnv(6, 10, 0, 0), false}, {"fri 9 done", liveEnv(4, 9, 30, 1), false},
		}},
		{"every Monday at 10am", []triggerCheck{
			{"mon 10", liveEnv(0, 10, 0, 0), true}, {"tue 10", liveEnv(1, 10, 0, 0), false},
			{"mon 9", liveEnv(0, 9, 0, 0), false},
		}},
		{"每天晚上8点半", []triggerCheck{
			{"20:29", liveEnv(0, 20, 29, 0), false}, {"20:30", liveEnv(0, 20, 30, 0), true},
			{"20:45 done", liveEnv(0, 20, 45, 1), false},
		}},
		{"每天上午9点和下午6点各一次", []triggerCheck{
			{"9:00", liveEnv(0, 9, 0, 0), true}, {"12 after one", liveEnv(0, 12, 0, 1), false},
			{"18 after one", liveEnv(0, 18, 0, 1), true}, {"19 after two", liveEnv(0, 19, 0, 2), false},
		}},
		{"每3小时一次", []triggerCheck{
			{"1h ago", withLastStarted(liveEnv(0, 12, 0, 1), time.Hour), false},
			{"3h ago", withLastStarted(liveEnv(0, 12, 0, 1), 3*time.Hour+time.Minute), true},
		}},
		{"用户空闲超过30分钟后整理一次对话", []triggerCheck{
			{"idle 10m", withIdle(liveEnv(0, 14, 0, 0), 10*time.Minute, false), false},
			{"idle 40m", withIdle(liveEnv(0, 14, 0, 0), 40*time.Minute, false), true},
			{"idle 40m already ran", withIdle(liveEnv(0, 14, 0, 1), 40*time.Minute, true), false},
		}},
		{"每月1号早上8点", []triggerCheck{
			{"nov 1 is past; dec 1 8:00", liveEnv(22, 8, 0, 0), true},
			{"nov 10 8:00", liveEnv(1, 8, 0, 0), false},
		}},
		{"周末中午12点", []triggerCheck{
			{"sat 12", liveEnv(5, 12, 0, 0), true}, {"sun 12", liveEnv(6, 12, 0, 0), true},
			{"mon 12", liveEnv(0, 12, 0, 0), false},
		}},
		{"凌晨2点", []triggerCheck{
			{"2:00", liveEnv(0, 2, 0, 0), true}, {"1:59", liveEnv(0, 1, 59, 0), false},
			{"2:30 done", liveEnv(0, 2, 30, 1), false},
		}},
		{"every weekday between 9am and 6pm, once an hour", []triggerCheck{
			{"tue 10 last 2h", withLastStarted(liveEnv(1, 10, 0, 1), 2*time.Hour), true},
			{"tue 10 last 10m", withLastStarted(liveEnv(1, 10, 0, 1), 10*time.Minute), false},
			{"tue 20", withLastStarted(liveEnv(1, 20, 0, 1), 2*time.Hour), false},
			{"sat 10", withLastStarted(liveEnv(5, 10, 0, 1), 2*time.Hour), false},
		}},
		{"每周一三五下午3点", []triggerCheck{
			{"wed 15", liveEnv(2, 15, 0, 0), true}, {"thu 15", liveEnv(3, 15, 0, 0), false},
			{"fri 15 done", liveEnv(4, 15, 5, 1), false},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			got, err := GenerateTrigger(context.Background(), client, tc.description, "")
			if err != nil {
				t.Fatalf("GenerateTrigger() error = %v", err)
			}
			t.Logf("%s => %s  (%s)", tc.description, got.Expression, got.Explanation)
			trigger, err := registry.CompileTrigger(got.Expression)
			if err != nil {
				t.Fatal(err)
			}
			for _, check := range tc.checks {
				ok, err := trigger.Evaluate(check.env)
				if err != nil {
					t.Errorf("%s: %v", check.name, err)
				} else if ok != check.want {
					t.Errorf("%s: got %v, want %v", check.name, ok, check.want)
				}
			}
		})
	}
}
