package weather

import (
	"context"
	"errors"
	"testing"
)

type testProvider struct {
	name        string
	observation Observation
	err         error
	calls       int
}

func (p *testProvider) Name() string { return p.name }

func (p *testProvider) Current(context.Context, Location) (Observation, error) {
	p.calls++
	return p.observation, p.err
}

func TestClientCurrentFallsBackToNextProvider(t *testing.T) {
	first := &testProvider{name: "first", err: errors.New("unavailable")}
	second := &testProvider{name: "second", observation: Observation{Condition: "晴", TemperatureC: 29}}

	got, err := NewClientWithProviders([]Provider{first, second}).Current(context.Background(), Location{Latitude: 22.5, Longitude: 114.1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "second" || got.Condition != "晴" || first.calls != 1 || second.calls != 1 {
		t.Fatalf("unexpected fallback result: %+v, calls: %d/%d", got, first.calls, second.calls)
	}
}

func TestClientCurrentCachesSuccessfulObservationForLocation(t *testing.T) {
	provider := &testProvider{name: "first", observation: Observation{Condition: "晴", TemperatureC: 29}}
	client := NewClientWithProviders([]Provider{provider})
	location := Location{Latitude: 22.5, Longitude: 114.1}

	first, err := client.Current(context.Background(), location)
	if err != nil {
		t.Fatal(err)
	}
	provider.observation = Observation{Condition: "雨", TemperatureC: 22}
	second, err := client.Current(context.Background(), location)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if second != first {
		t.Fatalf("cached observation = %+v, want %+v", second, first)
	}
}

func TestOpenMeteoCondition(t *testing.T) {
	if got := openMeteoCondition(95); got != "雷暴" {
		t.Fatalf("openMeteoCondition(95) = %q", got)
	}
}
