package loop

import "testing"

func TestCycleExecutorFacade(t *testing.T) {
	if (*Manager)(nil).Exec() != nil {
		t.Fatal("nil manager Exec")
	}
	m := &Manager{}
	ex := m.Exec()
	if ex == nil || ex.Manager() != m {
		t.Fatal("Exec binding")
	}
	_, _, _, err := (*CycleExecutor)(nil).CompleteOnce(nil, nil, RouteLocal, "", "", "", "", nil)
	if err == nil {
		t.Fatal("nil executor should error")
	}
}

func TestCheckGovernanceHardTokens(t *testing.T) {
	st := &LoopState{Spec: LoopSpec{Governance: GovernanceSpec{HardTokens: 10}}, Spend: BudgetSpend{Tokens: 11}}
	stop, reason := CheckGovernance(st, 0, 0)
	if !stop || reason != "budget_exceeded:hard_tokens" || !st.Spend.HardHit {
		t.Fatalf("stop=%v reason=%q hard=%v", stop, reason, st.Spend.HardHit)
	}
}
