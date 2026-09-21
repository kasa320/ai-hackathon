package config

import (
	"testing"
	"time"
)

func TestAgentModelsFallBackToDefault(t *testing.T) {
	t.Setenv("ORCAROUTER_MODEL", "base-model")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.OrcaRouterPlannerModel != "base-model" || c.OrcaRouterInterpreterModel != "base-model" {
		t.Fatalf("planner=%q interpreter=%q", c.OrcaRouterPlannerModel, c.OrcaRouterInterpreterModel)
	}
	if c.OrcaRouterTimeout != 180*time.Second {
		t.Fatalf("timeout=%v", c.OrcaRouterTimeout)
	}
}

func TestAgentModelsCanDiffer(t *testing.T) {
	t.Setenv("ORCAROUTER_PLANNER_MODEL", "planner-model")
	t.Setenv("ORCAROUTER_INTERPRETER_MODEL", "interpreter-model")
	t.Setenv("ORCAROUTER_TIMEOUT_SECONDS", "240")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.OrcaRouterPlannerModel != "planner-model" || c.OrcaRouterInterpreterModel != "interpreter-model" {
		t.Fatalf("planner=%q interpreter=%q", c.OrcaRouterPlannerModel, c.OrcaRouterInterpreterModel)
	}
	if c.OrcaRouterTimeout != 240*time.Second {
		t.Fatalf("timeout=%v", c.OrcaRouterTimeout)
	}
}

func TestInvalidTimeoutIsRejected(t *testing.T) {
	for _, v := range []string{"0", "-1", "601", "abc"} {
		t.Setenv("ORCAROUTER_TIMEOUT_SECONDS", v)
		if _, err := Load(); err == nil {
			t.Fatalf("%q が受け付けられました", v)
		}
	}
}
