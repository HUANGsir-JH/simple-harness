package config

import "testing"

// TestDefaultAPIKeyEnv 验证惯例环境变量名。
func TestDefaultAPIKeyEnv(t *testing.T) {
	if got := DefaultAPIKeyEnv; got != "ANTHROPIC_API_KEY" {
		t.Errorf("DefaultAPIKeyEnv: got %q want ANTHROPIC_API_KEY", got)
	}
}
