package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvironmentOverwritesExistingValues(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("HEPHAESTUS_LOCAL_MODEL_URL=http://new-model.example/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEPHAESTUS_LOCAL_MODEL_URL", "http://old-model.example/v1")

	if err := loadEnvironment(envFile); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("HEPHAESTUS_LOCAL_MODEL_URL"); got != "http://new-model.example/v1" {
		t.Fatalf("HEPHAESTUS_LOCAL_MODEL_URL = %q, want value from .env", got)
	}
}

func TestLoadEnvironmentClearsInheritedHephaestusValuesNotInFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("HEPHAESTUS_AUTH_USERNAME=deploy-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEPHAESTUS_LOCAL_MODEL_URL", "http://stale-model.example/v1")
	t.Setenv("UNRELATED_VALUE", "preserved")

	if err := loadEnvironment(envFile); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("HEPHAESTUS_LOCAL_MODEL_URL"); got != "" {
		t.Fatalf("HEPHAESTUS_LOCAL_MODEL_URL = %q, want cleared inherited value", got)
	}
	if got := os.Getenv("UNRELATED_VALUE"); got != "preserved" {
		t.Fatalf("UNRELATED_VALUE = %q, want preserved", got)
	}
}

func TestLoadEnvironmentAllowsMissingFile(t *testing.T) {
	if err := loadEnvironment(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEnvironmentReturnsParseErrors(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("not a dotenv assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := loadEnvironment(envFile); err == nil {
		t.Fatal("expected dotenv parse error")
	}
}
