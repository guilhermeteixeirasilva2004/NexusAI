// Package sqlitestore guarda sessões, memória longa e a auditoria de
// ferramentas num único arquivo SQLite. Implementa ports.SessionStore,
// ports.MemoryStore e ports.AuditLog.
package sqlitestore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"nexusai/internal/core/domain"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store é o banco do Nexus.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open abre (ou cria) o banco em path e aplica as migrações pendentes. O
// diretório é criado com permissão 0700 e o arquivo fica só para o dono.
func Open(ctx context.Context, path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("criar pasta do banco: %w", err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("abrir banco: %w", err)
		}
		f.Close()
	}
	db, err := driver.Open("file:"+path+"?_txlock=immediate", func(c *sqlite3.Conn) error {
		if err := fts5.Register(c); err != nil {
			return err
		}
		// WAL: uma transação confirmada sobrevive a kill -9 do processo.
		return c.Exec(`PRAGMA journal_mode = WAL;
			PRAGMA synchronous = NORMAL;
			PRAGMA foreign_keys = ON;
			PRAGMA busy_timeout = 5000;`)
	})
	if err != nil {
		return nil, fmt.Errorf("abrir banco: %w", err)
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1) // cada conexão teria o seu próprio banco
	}
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close fecha o banco.
func (s *Store) Close() error { return s.db.Close() }

// migrate aplica, em ordem, as migrações com número maior que user_version.
func (s *Store) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	type mig struct {
		version int
		name    string
	}
	var migs []mig
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return fmt.Errorf("migração com nome inválido: %s", e.Name())
		}
		migs = append(migs, mig{v, e.Name()})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })

	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("ler versão do banco: %w", err)
	}
	if n := len(migs); n > 0 && current > migs[n-1].version {
		return fmt.Errorf("o banco está na versão %d, mais nova que este nexus (%d)", current, migs[n-1].version)
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migração %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migração %s: %w", m.name, err)
		}
	}
	return nil
}

// Version devolve a versão de schema aplicada.
func (s *Store) Version(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// IntegrityCheck roda PRAGMA integrity_check e devolve erro se não for "ok".
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var res string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return errors.New(res)
	}
	return nil
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v) }

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Create implementa ports.SessionStore.
func (s *Store) Create(ctx context.Context) (domain.Session, error) {
	now := s.now()
	sess := domain.Session{ID: newID(), CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, created_at, updated_at) VALUES (?, ?, ?)`, sess.ID, ms(now), ms(now))
	if err != nil {
		return domain.Session{}, fmt.Errorf("criar sessão: %w", err)
	}
	return sess, nil
}

// Latest implementa ports.SessionStore.
func (s *Store) Latest(ctx context.Context) (domain.Session, error) {
	var sess domain.Session
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM sessions ORDER BY updated_at DESC, rowid DESC LIMIT 1`).
		Scan(&sess.ID, &sess.Title, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("última sessão: %w", err)
	}
	sess.CreatedAt, sess.UpdatedAt = fromMS(created), fromMS(updated)
	return sess, nil
}

// Append implementa ports.SessionStore: todas as mensagens numa transação.
func (s *Store) Append(ctx context.Context, sessionID string, msgs ...domain.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gravar mensagens: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	// updated_at cresce sempre, mesmo com várias escritas no mesmo milissegundo,
	// para que Latest seja a sessão usada por último.
	res, err := tx.ExecContext(ctx, `UPDATE sessions SET updated_at = max(?, (SELECT max(updated_at) FROM sessions) + 1) WHERE id = ?`, ms(now), sessionID)
	if err != nil {
		return fmt.Errorf("gravar mensagens: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("sessão %q: %w", sessionID, domain.ErrNotFound)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO messages (session_id, origin, content, tool_name, created_at) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range msgs {
		created := m.CreatedAt
		if created.IsZero() {
			created = now
		}
		if _, err := stmt.ExecContext(ctx, sessionID, string(m.Origin), m.Content, m.ToolName, ms(created)); err != nil {
			return fmt.Errorf("gravar mensagem: %w", err)
		}
	}
	return tx.Commit()
}

// History implementa ports.SessionStore.
func (s *Store) History(ctx context.Context, sessionID string, limit int) ([]domain.Message, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE id = ?`, sessionID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, fmt.Errorf("sessão %q: %w", sessionID, domain.ErrNotFound)
	}
	if limit <= 0 {
		limit = -1 // sem limite no SQLite
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, origin, content, tool_name, created_at FROM (
			SELECT * FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("ler histórico: %w", err)
	}
	defer rows.Close()
	var out []domain.Message
	for rows.Next() {
		var m domain.Message
		var origin string
		var created int64
		if err := rows.Scan(&m.ID, &origin, &m.Content, &m.ToolName, &created); err != nil {
			return nil, err
		}
		m.SessionID, m.Origin, m.CreatedAt = sessionID, domain.Origin(origin), fromMS(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountMessages conta as mensagens de uma sessão por origem.
func (s *Store) CountMessages(ctx context.Context, sessionID string) (map[domain.Origin]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT origin, count(*) FROM messages WHERE session_id = ? GROUP BY origin`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.Origin]int{}
	for rows.Next() {
		var o string
		var n int
		if err := rows.Scan(&o, &n); err != nil {
			return nil, err
		}
		out[domain.Origin(o)] = n
	}
	return out, rows.Err()
}

// Save implementa ports.MemoryStore.
func (s *Store) Save(ctx context.Context, item domain.MemoryItem) (domain.MemoryItem, error) {
	item.Text = strings.TrimSpace(item.Text)
	if item.Text == "" {
		return domain.MemoryItem{}, errors.New("memória vazia")
	}
	if item.Trust == "" {
		item.Trust = domain.TrustLow
	}
	if item.Kind == "" {
		item.Kind = "fact"
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = s.now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO memories (text, kind, source, trust, created_at) VALUES (?, ?, ?, ?, ?)`,
		item.Text, item.Kind, item.Source, string(item.Trust), ms(item.CreatedAt))
	if err != nil {
		return domain.MemoryItem{}, fmt.Errorf("guardar memória: %w", err)
	}
	item.ID, err = res.LastInsertId()
	return item, err
}

// Search implementa ports.MemoryStore com FTS5 e ranking bm25. A consulta é
// montada só com palavras entre aspas: nada do texto vira operador do FTS.
func (s *Store) Search(ctx context.Context, q domain.MemoryQuery) ([]domain.MemoryItem, error) {
	match := ftsQuery(q.Text)
	if match == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.text, m.kind, m.source, m.trust, m.created_at
		FROM memories_fts f JOIN memories m ON m.id = f.rowid
		WHERE memories_fts MATCH ?
		ORDER BY bm25(memories_fts), m.id DESC
		LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("buscar memória: %w", err)
	}
	defer rows.Close()
	var out []domain.MemoryItem
	for rows.Next() {
		var it domain.MemoryItem
		var trust string
		var created int64
		if err := rows.Scan(&it.ID, &it.Text, &it.Kind, &it.Source, &trust, &created); err != nil {
			return nil, err
		}
		it.Trust, it.CreatedAt = domain.Trust(trust), fromMS(created)
		out = append(out, it)
	}
	return out, rows.Err()
}

// stopwords comuns em português que só atrapalham o ranking.
var stopwords = map[string]bool{
	"que": true, "qual": true, "quais": true, "uma": true, "uns": true, "umas": true, "para": true, "por": true,
	"com": true, "sem": true, "dos": true, "das": true, "nos": true, "nas": true, "como": true, "onde": true,
	"quando": true, "meu": true, "minha": true, "seu": true, "sua": true, "isso": true, "este": true, "esta": true,
	"esse": true, "essa": true, "the": true, "and": true, "voce": true, "você": true, "sobre": true, "tem": true,
}

func ftsQuery(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	seen := map[string]bool{}
	var terms []string
	for _, w := range words {
		if len([]rune(w)) < 3 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
		if len(terms) == 16 {
			break
		}
	}
	return strings.Join(terms, " OR ")
}

// RecordToolRun implementa ports.AuditLog.
func (s *Store) RecordToolRun(ctx context.Context, r domain.ToolRun) error {
	created := r.CreatedAt
	if created.IsZero() {
		created = s.now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tool_runs (session_id, trace_id, tool, args, risk, decision, result, error, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.SessionID, r.TraceID, r.Tool, r.Args, string(r.Risk), r.Decision, r.Result, r.Error, r.Duration.Milliseconds(), ms(created))
	if err != nil {
		return fmt.Errorf("auditar ferramenta: %w", err)
	}
	return nil
}

// ToolRuns devolve a auditoria de uma sessão, em ordem.
func (s *Store) ToolRuns(ctx context.Context, sessionID string) ([]domain.ToolRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT trace_id, tool, args, risk, decision, result, error, duration_ms, created_at
		FROM tool_runs WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ToolRun
	for rows.Next() {
		var r domain.ToolRun
		var risk string
		var dur, created int64
		if err := rows.Scan(&r.TraceID, &r.Tool, &r.Args, &risk, &r.Decision, &r.Result, &r.Error, &dur, &created); err != nil {
			return nil, err
		}
		r.SessionID, r.Risk = sessionID, domain.Risk(risk)
		r.Duration, r.CreatedAt = time.Duration(dur)*time.Millisecond, fromMS(created)
		out = append(out, r)
	}
	return out, rows.Err()
}
