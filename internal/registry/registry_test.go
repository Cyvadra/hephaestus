package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLoad_ValidConfigs(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "identity-default.toml", `
name = "default"
description = "test identity"
preferred_model = "deepseek-v4-flash"
reasoning_effort = "none"
context_window_tokens = 128000
max_tokens = 1024
system_prompt = "You're a helpful assistant."
`)
	writeFile(t, dir, "impression-work.toml", `
name = "work"
description = "test impression"
enabled = true

[[messages]]
role = "user"
content = "remember this"
`)
	writeFile(t, dir, "toolgroup-basic.yaml", `
name: basic
tools:
  - shell
`)
	writeFile(t, dir, "concierge-coding.yaml", `
name: coding
identity: default
impressions:
  - work
tool_groups:
  - basic
plugins: []
`)
	writeFile(t, dir, "workflow-daily-summary.yaml", `
name: daily-summary
description: summarize the day
concierge: coding
steps:
  - do the thing
`)
	writeFile(t, dir, "job-morning-brief.yaml", `
name: morning-brief
title: Morning brief
workflows:
  - workflow: daily-summary
    retry_delay_seconds: 60
    retry_count: 2
trigger: "true"
max_executions_per_day: 1
`)

	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, ok := reg.Identities["default"]; !ok {
		t.Error("expected identity 'default' to be loaded")
	}
	if _, ok := reg.Impressions["work"]; !ok {
		t.Error("expected impression 'work' to be loaded")
	}
	if _, ok := reg.ToolGroups["basic"]; !ok {
		t.Error("expected tool group 'basic' to be loaded")
	}
	if _, ok := reg.Concierges["coding"]; !ok {
		t.Error("expected concierge 'coding' to be loaded")
	}
	if _, ok := reg.Workflows["daily-summary"]; !ok {
		t.Error("expected workflow 'daily-summary' to be loaded")
	}
	if _, ok := reg.Jobs["morning-brief"]; !ok {
		t.Error("expected job 'morning-brief' to be loaded")
	}

	if err := reg.Validate(map[string]bool{"shell": true}, map[string]bool{}); err != nil {
		t.Errorf("Validate: unexpected error: %v", err)
	}
}

func TestLoad_RepositoryConfigExamples(t *testing.T) {
	reg, err := Load(filepath.Join("..", "..", "config"))
	if err != nil {
		t.Fatalf("Load repository config: %v", err)
	}

	knownTools := map[string]bool{
		"chat_history_search": true,
		"create_project":      true, "list_projects": true,
		"web_fetch": true, "web_search": true, "shell": true,
	}
	if err := reg.Validate(knownTools, map[string]bool{}); err != nil {
		t.Fatalf("Validate repository config: %v", err)
	}

	identity := reg.Identities["example"]
	if identity.Temperature == nil || identity.TopP == nil || len(identity.InjectedMessages) != 2 {
		t.Fatalf("expected field-complete example identity, got %+v", identity)
	}
	impression := reg.Impressions["example"]
	if impression.Enabled || len(impression.Messages) != 2 {
		t.Fatalf("expected disabled example impression with two messages, got %+v", impression)
	}
	workflow := reg.Workflows["example-workflow"]
	if workflow.InputSchema == nil || workflow.OutputSchema == nil || len(workflow.Steps) != 2 {
		t.Fatalf("expected field-complete example workflow, got %+v", workflow)
	}
	job := reg.Jobs["example-job"]
	if len(job.Workflows) != 1 || job.Workflows[0].RetryDelaySeconds != 60 || job.Workflows[0].RetryCount != 2 {
		t.Fatalf("expected field-complete example job, got %+v", job)
	}
}

func TestLoad_NameMismatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "identity-default.toml", `
name = "not-default"
system_prompt = "hi"
`)

	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for filename/name mismatch")
	}
}

func TestLoad_BadReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "identity-default.toml", `
name = "default"
reasoning_effort = "extreme"
`)

	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for invalid reasoning_effort")
	}
}

func TestValidate_UnknownToolGroupReference(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "toolgroup-basic.yaml", `
name: basic
tools:
  - nonexistent_tool
`)

	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := reg.Validate(map[string]bool{}, map[string]bool{}); err == nil {
		t.Fatal("expected validation error for unknown tool reference")
	}
}
