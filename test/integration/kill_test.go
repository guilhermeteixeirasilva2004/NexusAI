package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nexusai/internal/adapters/outbound/sqlitestore"
	"nexusai/internal/core/cognition"
	"nexusai/internal/core/domain"
	"nexusai/internal/core/fakes"
	"nexusai/internal/core/orchestrator"
	"nexusai/internal/core/policy"
	"nexusai/internal/core/ports"
)

const helperEnv = "NEXUS_KILL_HELPER_DB"

// TestTurnsSurviveKill9 é a meta da Fase 1: um processo grava turnos sem
// parar (cada um com chamada de ferramenta), leva SIGKILL num momento
// aleatório e o banco reaberto precisa ter só turnos completos, pelo menos 20.
func TestTurnsSurviveKill9(t *testing.T) {
	if testing.Short() {
		t.Skip("lento")
	}
	for round := 0; round < 3; round++ {
		t.Run(fmt.Sprintf("rodada %d", round+1), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "nexus.db")
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWriter$")
			cmd.Env = append(os.Environ(), helperEnv+"="+db)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Mata depois de 20 a 40 turnos confirmados, mais um atraso
			// aleatório, para cair no meio de uma escrita.
			target := 20 + rand.IntN(21)
			sc := bufio.NewScanner(stdout)
			confirmed := 0
			for sc.Scan() {
				if strings.HasPrefix(sc.Text(), "turno ") {
					confirmed++
					if confirmed >= target {
						break
					}
				}
			}
			time.Sleep(time.Duration(rand.IntN(3000)) * time.Microsecond)
			if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			if confirmed < 20 {
				t.Fatalf("o processo só confirmou %d turnos antes de morrer", confirmed)
			}

			ctx := context.Background()
			store, err := sqlitestore.Open(ctx, db)
			if err != nil {
				t.Fatalf("reabrir depois do kill -9: %v", err)
			}
			defer store.Close()
			if err := store.IntegrityCheck(ctx); err != nil {
				t.Fatalf("integridade: %v", err)
			}
			sess, err := store.Latest(ctx)
			if err != nil {
				t.Fatal(err)
			}
			hist, err := store.History(ctx, sess.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Turno completo = pessoa, chamada, resultado, resposta final.
			if len(hist)%4 != 0 {
				t.Fatalf("turno pela metade: %d mensagens", len(hist))
			}
			turns := len(hist) / 4
			for i := 0; i < turns; i++ {
				m := hist[i*4 : i*4+4]
				if m[0].Origin != domain.OriginUser || m[1].ToolName != "read_file" || m[2].Origin != domain.OriginTool ||
					m[3].Origin != domain.OriginAssistant || m[3].Content != fmt.Sprintf("resposta %d", i+1) {
					t.Fatalf("turno %d malformado: %+v", i+1, m)
				}
			}
			if turns < confirmed {
				t.Fatalf("%d turnos confirmados, mas só %d no banco", confirmed, turns)
			}
			runs, _ := store.ToolRuns(ctx, sess.ID)
			// A auditoria é gravada antes do fim do turno: pode ter um a mais.
			if len(runs) < turns || len(runs) > turns+1 {
				t.Errorf("auditoria: %d registros para %d turnos", len(runs), turns)
			}
			t.Logf("kill -9 depois de %d turnos confirmados; %d turnos completos no banco, %d auditorias", confirmed, turns, len(runs))
		})
	}
}

// TestHelperWriter é o processo que leva o kill -9. Só roda como filho.
func TestHelperWriter(t *testing.T) {
	db := os.Getenv(helperEnv)
	if db == "" {
		t.Skip("só roda como processo filho de TestTurnsSurviveKill9")
	}
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	turn := 0
	model := &fakes.Model{Chunk: 1000, Respond: func(req ports.GenerateRequest) string {
		last := req.Messages[len(req.Messages)-1].Content
		if strings.Contains(last, "<dado") {
			return fmt.Sprintf("resposta %d", turn)
		}
		return `{"tool": "read_file", "args": {"path": "a.txt"}}`
	}}
	read := &fakes.Tool{
		SpecValue: domain.ToolSpec{Name: "read_file", Params: json.RawMessage(`{"type":"object"}`), Risk: domain.RiskRead},
		Result:    domain.ToolResult{Content: strings.Repeat("conteúdo ", 200)},
	}
	o := &orchestrator.Orchestrator{
		Models:   map[domain.Role]ports.ChatModel{domain.RoleMain: model},
		Sessions: store, Memory: store, Audit: store,
		Tools: []ports.Tool{read}, Codec: cognition.JSONCodec{}, Policy: policy.New("read_file"),
	}
	for {
		turn++
		if _, err := o.Ask(ctx, sess.ID, fmt.Sprintf("pergunta %d", turn), nil); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("turno %d\n", turn)
	}
}
