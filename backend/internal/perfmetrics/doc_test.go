package perfmetrics

import "testing"

func TestScenarioNamesStable(t *testing.T) {
	if len(ScenarioNames) != 8 {
		t.Fatalf("expected 8 scenarios, got %d", len(ScenarioNames))
	}
}
