package sqlitestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/ports"
	"nexusai/internal/core/ports/contract"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "sub", "nexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionContract(t *testing.T) {
	contract.SessionStore(t, func(t *testing.T) ports.SessionStore { return open(t) })
}

func TestMemoryContract(t *testing.T) {
	contract.MemoryStore(t, func(t *testing.T) ports.MemoryStore { return open(t) })
}

func TestInMemory(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsAndPermissions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "x", "nexus.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.Version(ctx); err != nil || v != 1 {
		t.Fatalf("versão = %d, %v", v, err)
	}
	if err := s.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	s.Close()

	st, _ := os.Stat(path)
	dst, _ := os.Stat(filepath.Dir(path))
	if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
		t.Errorf("permissões: arquivo %v, pasta %v", st.Mode().Perm(), dst.Mode().Perm())
	}

	// Reabrir não reaplica migrações.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "mais nova") {
		t.Errorf("banco do futuro deveria ser recusado: %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	f := filepath.Join(t.TempDir(), "arquivo")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), filepath.Join(f, "nexus.db")); err == nil {
		t.Error("pasta que é arquivo deveria falhar")
	}
}

func TestSearchAccentsAndRanking(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, text := range []string{"Gosta de café coado", "Trabalha com Go e SQLite", "Tem um gato chamado Café"} {
		if _, err := s.Save(ctx, domain.MemoryItem{Text: text, Source: "user", Trust: domain.TrustHigh}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.Search(ctx, domain.MemoryQuery{Text: "cafe coado", Limit: 5})
	if err != nil || len(hits) != 2 || hits[0].Text != "Gosta de café coado" {
		t.Fatalf("sem acento e ranking: %+v, %v", hits, err)
	}
	if hits[0].Kind != "fact" || hits[0].CreatedAt.IsZero() {
		t.Errorf("padrões de Save: %+v", hits[0])
	}
	if got := ftsQuery("Qual é o meu café? café!"); got != `"café"` {
		t.Errorf("ftsQuery = %q", got)
	}
	if hits, _ := s.Search(ctx, domain.MemoryQuery{Text: "o é"}); hits != nil {
		t.Error("consulta só com palavras curtas não busca nada")
	}
	if _, err := s.Save(ctx, domain.MemoryItem{Text: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestAudit(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	run := domain.ToolRun{SessionID: "s1", TraceID: "t1", Tool: "read_file", Args: `{"path":"a"}`, Risk: domain.RiskRead,
		Decision: "allowed", Result: "conteúdo", Duration: 15 * time.Millisecond}
	if err := s.RecordToolRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.Decision, run.Error = "denied_user", "x"
	if err := s.RecordToolRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ToolRuns(ctx, "s1")
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %d, %v", len(runs), err)
	}
	if runs[0].Decision != "allowed" || runs[0].Duration != 15*time.Millisecond || runs[1].Error != "x" || runs[0].CreatedAt.IsZero() {
		t.Errorf("runs = %+v", runs)
	}
}

func TestCountMessagesAndClock(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	fixed := time.UnixMilli(1000)
	s.now = func() time.Time { return fixed }
	a, _ := s.Create(ctx)
	b, _ := s.Create(ctx)
	// Mesmo com relógio parado, a sessão atualizada por último vence.
	if err := s.Append(ctx, a.ID, domain.Message{Origin: domain.OriginUser, Content: "1"}, domain.Message{Origin: domain.OriginAssistant, Content: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, b.ID, domain.Message{Origin: domain.OriginUser, Content: "3"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, a.ID, domain.Message{Origin: domain.OriginUser, Content: "4"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Latest(ctx); l.ID != a.ID {
		t.Errorf("Latest = %s, quer %s", l.ID, a.ID)
	}
	counts, err := s.CountMessages(ctx, a.ID)
	if err != nil || counts[domain.OriginUser] != 2 || counts[domain.OriginAssistant] != 1 {
		t.Errorf("contagem = %v, %v", counts, err)
	}
	h, _ := s.History(ctx, a.ID, 0)
	if !h[0].CreatedAt.Equal(fixed) {
		t.Errorf("created_at = %v", h[0].CreatedAt)
	}
}
