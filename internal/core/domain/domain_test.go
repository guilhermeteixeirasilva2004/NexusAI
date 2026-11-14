package domain

import "testing"

func TestRoles(t *testing.T) {
	for _, r := range Roles() {
		if !r.Valid() {
			t.Errorf("%s deveria ser válido", r)
		}
	}
	if Role("big").Valid() {
		t.Error("papel desconhecido")
	}
}

func TestOriginAndRisk(t *testing.T) {
	trusted := map[Origin]bool{OriginSystem: true, OriginUser: true, OriginAssistant: false, OriginTool: false, OriginMemory: false}
	for o, want := range trusted {
		if o.Trusted() != want {
			t.Errorf("%s.Trusted() = %v", o, !want)
		}
	}
	if RiskRead.NeedsConfirmation() || !RiskWrite.NeedsConfirmation() || !RiskDestructive.NeedsConfirmation() {
		t.Error("NeedsConfirmation")
	}
}
