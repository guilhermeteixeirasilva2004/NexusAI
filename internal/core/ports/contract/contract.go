// Package contract tem as suítes de contrato das portas. Toda implementação
// de uma porta (fake ou adapter) roda a mesma suíte: é o Liskov na prática.
package contract

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"nexusai/internal/core/domain"
	"nexusai/internal/core/ports"
)

// SessionStore roda o contrato de ports.SessionStore. newStore deve devolver
// um store vazio a cada chamada.
func SessionStore(t *testing.T, newStore func(t *testing.T) ports.SessionStore) {
	ctx := context.Background()

	t.Run("latest vazio", func(t *testing.T) {
		if _, err := newStore(t).Latest(ctx); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Latest sem sessões = %v, quer ErrNotFound", err)
		}
	})

	t.Run("append e history em ordem", func(t *testing.T) {
		s := newStore(t)
		sess, err := s.Create(ctx)
		if err != nil || sess.ID == "" {
			t.Fatalf("Create = %+v, %v", sess, err)
		}
		var msgs []domain.Message
		for i := 0; i < 5; i++ {
			msgs = append(msgs, domain.Message{Origin: domain.OriginUser, Content: fmt.Sprintf("m%d", i)})
		}
		msgs[2] = domain.Message{Origin: domain.OriginTool, Content: "resultado", ToolName: "read_file"}
		if err := s.Append(ctx, sess.ID, msgs[:3]...); err != nil {
			t.Fatal(err)
		}
		if err := s.Append(ctx, sess.ID, msgs[3:]...); err != nil {
			t.Fatal(err)
		}
		all, err := s.History(ctx, sess.ID, 0)
		if err != nil || len(all) != 5 {
			t.Fatalf("History = %d, %v", len(all), err)
		}
		for i, m := range all {
			if m.Content != msgs[i].Content || m.Origin != msgs[i].Origin || m.ToolName != msgs[i].ToolName || m.SessionID != sess.ID || m.ID == 0 {
				t.Errorf("mensagem %d = %+v", i, m)
			}
		}
		last, err := s.History(ctx, sess.ID, 2)
		if err != nil || len(last) != 2 || last[0].Content != "m3" || last[1].Content != "m4" {
			t.Fatalf("History(2) = %+v, %v", last, err)
		}
	})

	t.Run("latest é a atualizada por último", func(t *testing.T) {
		s := newStore(t)
		a, _ := s.Create(ctx)
		b, _ := s.Create(ctx)
		if err := s.Append(ctx, a.ID, domain.Message{Origin: domain.OriginUser, Content: "oi"}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Latest(ctx)
		if err != nil || got.ID != a.ID {
			t.Fatalf("Latest = %s, %v; quer %s (b = %s)", got.ID, err, a.ID, b.ID)
		}
	})

	t.Run("sessão inexistente", func(t *testing.T) {
		s := newStore(t)
		if err := s.Append(ctx, "nao-existe", domain.Message{Origin: domain.OriginUser, Content: "x"}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Append = %v", err)
		}
		if _, err := s.History(ctx, "nao-existe", 10); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("History = %v", err)
		}
	})
}

// MemoryStore roda o contrato de ports.MemoryStore.
func MemoryStore(t *testing.T, newStore func(t *testing.T) ports.MemoryStore) {
	ctx := context.Background()

	t.Run("save e search", func(t *testing.T) {
		s := newStore(t)
		facts := []string{"A pessoa prefere café sem açúcar", "O projeto se chama Nexus", "Mora em Belo Horizonte"}
		for _, f := range facts {
			item, err := s.Save(ctx, domain.MemoryItem{Text: f, Kind: "fact", Source: "user", Trust: domain.TrustHigh})
			if err != nil || item.ID == 0 {
				t.Fatalf("Save = %+v, %v", item, err)
			}
		}
		hits, err := s.Search(ctx, domain.MemoryQuery{Text: "qual café a pessoa prefere?", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 || hits[0].Text != facts[0] {
			t.Fatalf("Search café = %+v", hits)
		}
		if hits[0].Trust != domain.TrustHigh || hits[0].Kind != "fact" || hits[0].Source != "user" {
			t.Errorf("campos perdidos: %+v", hits[0])
		}
		none, err := s.Search(ctx, domain.MemoryQuery{Text: "xyzw", Limit: 5})
		if err != nil || len(none) != 0 {
			t.Errorf("Search sem relação = %+v, %v", none, err)
		}
	})

	t.Run("limite", func(t *testing.T) {
		s := newStore(t)
		for i := 0; i < 8; i++ {
			if _, err := s.Save(ctx, domain.MemoryItem{Text: fmt.Sprintf("nota sobre gatos %d", i), Kind: "fact", Source: "user", Trust: domain.TrustLow}); err != nil {
				t.Fatal(err)
			}
		}
		hits, err := s.Search(ctx, domain.MemoryQuery{Text: "gatos", Limit: 3})
		if err != nil || len(hits) != 3 {
			t.Fatalf("Search limite 3 = %d, %v", len(hits), err)
		}
	})

	t.Run("texto com símbolos não quebra a busca", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Save(ctx, domain.MemoryItem{Text: "usa o editor vim", Kind: "fact", Source: "user", Trust: domain.TrustHigh}); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{`"vim" OR 1=1 --`, "NEAR(vim*)", "', \"); DROP TABLE memories; --", ""} {
			if _, err := s.Search(ctx, domain.MemoryQuery{Text: q, Limit: 3}); err != nil {
				t.Errorf("Search(%q) = %v", q, err)
			}
		}
	})

	t.Run("memória vazia é recusada", func(t *testing.T) {
		if _, err := newStore(t).Save(ctx, domain.MemoryItem{Text: "  ", Kind: "fact", Source: "user", Trust: domain.TrustHigh}); err == nil {
			t.Error("Save vazio deveria falhar")
		}
	})
}
