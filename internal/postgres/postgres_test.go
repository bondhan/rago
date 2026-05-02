package postgres

import (
	"os"
	"testing"
)

func TestGetEnvReturnsValue(t *testing.T) {
	os.Setenv("TEST_RAGO_KEY", "hello")
	defer os.Unsetenv("TEST_RAGO_KEY")

	if v := getEnv("TEST_RAGO_KEY", "fallback"); v != "hello" {
		t.Errorf("expected 'hello', got %q", v)
	}
}

func TestGetEnvReturnsFallback(t *testing.T) {
	os.Unsetenv("TEST_RAGO_MISSING")

	if v := getEnv("TEST_RAGO_MISSING", "default"); v != "default" {
		t.Errorf("expected 'default', got %q", v)
	}
}

func TestGetEnvIntReturnsInt(t *testing.T) {
	os.Setenv("TEST_RAGO_INT", "42")
	defer os.Unsetenv("TEST_RAGO_INT")

	if v := getEnvInt("TEST_RAGO_INT", 0); v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
}

func TestGetEnvIntReturnsFallback(t *testing.T) {
	os.Unsetenv("TEST_RAGO_INT_MISSING")

	if v := getEnvInt("TEST_RAGO_INT_MISSING", 99); v != 99 {
		t.Errorf("expected 99, got %d", v)
	}
}

func TestGetEnvIntReturnsFallbackOnInvalidValue(t *testing.T) {
	os.Setenv("TEST_RAGO_INT_BAD", "not-a-number")
	defer os.Unsetenv("TEST_RAGO_INT_BAD")

	if v := getEnvInt("TEST_RAGO_INT_BAD", 7); v != 7 {
		t.Errorf("expected 7, got %d", v)
	}
}
