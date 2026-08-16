package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
default_tool_groups:
  - basic
plugins: []
default_plugins: []
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
    project: default-workspace
    input: {}
    max_attempts: 3
    retry_delay_seconds: 60
trigger: "true"
max_executions_per_day: 1
`)
	writeFile(t, dir, "constant-proxy_url.toml", `
name = "proxy_url"
value = "https://proxy.example.test"
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
	if got := reg.Constants["proxy_url"].Value; got != "https://proxy.example.test" {
		t.Errorf("constant proxy_url = %q, want configured value", got)
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
		"chat_history_read":   true,
		"create_project":      true, "list_projects": true,
		"web_fetch": true, "web_search": true, "shell": true, "send_file": true,
		"send_notification": true,
		"spawn":             true, "fork": true, "await": true,
	}
	if err := reg.Validate(knownTools, map[string]bool{"metaphysics": true}); err != nil {
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
	if len(job.Workflows) != 1 || job.Workflows[0].MaxAttempts != 3 || job.Workflows[0].RetryDelaySeconds != 60 {
		t.Fatalf("expected field-complete example job, got %+v", job)
	}
}

func TestValidateConciergeDefaultsMustBeAvailable(t *testing.T) {
	reg := &Registry{
		Identities: map[string]Identity{"default": {Name: "default"}},
		ToolGroups: map[string]ToolGroup{"basic": {Name: "basic"}, "optional": {Name: "optional"}},
		Concierges: map[string]Concierge{
			"coding": {
				Name:              "coding",
				Identity:          "default",
				ToolGroups:        []string{"basic"},
				DefaultToolGroups: []string{"optional"},
			},
		},
	}
	if err := reg.Validate(map[string]bool{}, map[string]bool{}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Validate() error = %v, want unavailable default tool group", err)
	}
}

func TestLoadTemplates_UsesSemanticContentHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "toolgroup-basic.yaml")
	writeFile(t, dir, "toolgroup-basic.yaml", "name: basic\ntools: [shell]\n")
	firstTime := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, firstTime, firstTime); err != nil {
		t.Fatalf("set first mtime: %v", err)
	}

	_, first, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("LoadTemplates first: %v", err)
	}
	if len(first) != 1 || first[0].Kind != KindToolGroup || first[0].Name != "basic" || first[0].Path != "toolgroup-basic.yaml" {
		t.Fatalf("unexpected template metadata: %+v", first)
	}
	if !first[0].ModifiedAt.Equal(firstTime) {
		t.Fatalf("expected mtime %v, got %v", firstTime, first[0].ModifiedAt)
	}

	writeFile(t, dir, "toolgroup-basic.yaml", "name: basic\ntools:\n  - shell\n")
	secondTime := firstTime.Add(time.Hour)
	if err := os.Chtimes(path, secondTime, secondTime); err != nil {
		t.Fatalf("set second mtime: %v", err)
	}
	_, second, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("LoadTemplates second: %v", err)
	}
	if first[0].Hash != second[0].Hash {
		t.Fatalf("format-only change altered semantic hash: %s != %s", first[0].Hash, second[0].Hash)
	}

	writeFile(t, dir, "toolgroup-basic.yaml", "name: basic\ntools: [web_search]\n")
	_, third, err := LoadTemplates(dir)
	if err != nil {
		t.Fatalf("LoadTemplates third: %v", err)
	}
	if second[0].Hash == third[0].Hash {
		t.Fatal("business content change did not alter semantic hash")
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

// loadRegistry builds a Registry from config files keyed by filename.
func loadRegistry(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return reg
}

// minimalConcierge files make Validate's cross-record checks reach job logic.
var minimalConcierge = map[string]string{
	"identity-default.toml": `
name = "default"
system_prompt = "You're a helpful assistant."
context_window_tokens = 128000
`,
	"concierge-coding.yaml": `
name: coding
identity: default
impressions: []
tool_groups: []
plugins: []
`,
}

// loadErr builds config files in a temp dir and returns the Load error.
func loadErr(t *testing.T, files map[string]string) error {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	_, err := Load(dir)
	return err
}

func TestLoad_BlankWorkflowStepRejected(t *testing.T) {
	files := map[string]string{
		"workflow-blank-step.yaml": `
name: blank-step-workflow
description: bad
concierge: coding
steps:
  - "   "
`,
	}
	if err := loadErr(t, files); err == nil {
		t.Fatal("expected error for blank workflow step")
	}
}

func TestLoad_InvalidWorkflowSchemaRejected(t *testing.T) {
	files := map[string]string{
		"workflow-bad-schema.yaml": `
name: bad-schema-workflow
description: bad
concierge: coding
input_schema:
  type: not-a-type
steps:
  - do it
`,
	}
	if err := loadErr(t, files); err == nil {
		t.Fatal("expected error for invalid workflow schema")
	}
}

func TestLoad_JobMaxAttemptsBelowOneRejected(t *testing.T) {
	files := map[string]string{
		"job-bad-attempts.yaml": `
name: bad-attempts-job
workflows:
  - workflow: some-workflow
    project: default-workspace
    input: {}
    max_attempts: 0
trigger: "true"
max_executions_per_day: 1
`,
	}
	if err := loadErr(t, files); err == nil {
		t.Fatal("expected error for max_attempts < 1")
	}
}

func TestLoad_JobInvalidTriggerRejected(t *testing.T) {
	files := map[string]string{
		"job-bad-trigger.yaml": `
name: bad-trigger-job
workflows:
  - workflow: some-workflow
    project: default-workspace
    input: {}
    max_attempts: 1
trigger: "Hour + 1"
max_executions_per_day: 1
`,
	}
	if err := loadErr(t, files); err == nil {
		t.Fatal("expected error for non-boolean trigger")
	}
}

func TestLoad_JobUnknownPlaceholderRejected(t *testing.T) {
	files := map[string]string{
		"job-bad-placeholder.yaml": `
name: bad-placeholder-job
workflows:
  - workflow: some-workflow
    project: default-workspace
    input:
      topic: "${unknown.var}"
    max_attempts: 1
trigger: "true"
max_executions_per_day: 1
`,
	}
	if err := loadErr(t, files); err == nil {
		t.Fatal("expected error for unknown placeholder")
	}
}

func TestValidate_JobBindingMissingRequiredKey(t *testing.T) {
	files := map[string]string{}
	for name, content := range minimalConcierge {
		files[name] = content
	}
	files["workflow-needs-topic.yaml"] = `
name: needs-topic
description: needs topic
concierge: coding
input_schema:
  type: object
  properties:
    topic:
      type: string
  required:
    - topic
steps:
  - do it
`
	files["job-missing-key.yaml"] = `
name: missing-key
workflows:
  - workflow: needs-topic
    project: default-workspace
    input:
      other: "${job.goal}"
    max_attempts: 1
trigger: "true"
max_executions_per_day: 1
`
	reg := loadRegistry(t, files)
	err := reg.Validate(map[string]bool{}, map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "missing required key") {
		t.Fatalf("expected missing required key error, got %v", err)
	}
}

func TestValidate_JobBindingLiteralInputMismatch(t *testing.T) {
	files := map[string]string{}
	for name, content := range minimalConcierge {
		files[name] = content
	}
	files["workflow-needs-topic.yaml"] = `
name: needs-topic
description: needs topic
concierge: coding
input_schema:
  type: object
  properties:
    topic:
      type: string
  required:
    - topic
steps:
  - do it
`
	files["job-bad-input.yaml"] = `
name: bad-input
workflows:
  - workflow: needs-topic
    project: default-workspace
    input:
      topic: 42
    max_attempts: 1
trigger: "true"
max_executions_per_day: 1
`
	reg := loadRegistry(t, files)
	err := reg.Validate(map[string]bool{}, map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "does not satisfy input schema") {
		t.Fatalf("expected input schema mismatch error, got %v", err)
	}
}
