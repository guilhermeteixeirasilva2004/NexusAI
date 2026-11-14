// Package config carrega e valida a configuração do Nexus.
//
// Ordem de precedência: valores padrão, depois o arquivo JSON, depois variáveis
// de ambiente NEXUS_*. Campos desconhecidos no arquivo são erro, para que um
// erro de digitação não passe calado.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nexusai/internal/core/domain"
)

// Config é a configuração completa do processo nexus.
type Config struct {
	Server  ServerConfig                `json:"server"`
	Models  map[domain.Role]ModelConfig `json:"models"`
	Loop    LoopConfig                  `json:"loop"`
	Storage StorageConfig               `json:"storage"`
	Log     LogConfig                   `json:"log"`

	// Source é o arquivo de config usado; vazio quando só valem os padrões.
	Source string `json:"-"`
}

// ServerConfig define onde a API do nexus escuta.
type ServerConfig struct {
	// Listen são endereços host:port. Só IPs explícitos: loopback ou a
	// interface WireGuard. 0.0.0.0 e :: são recusados.
	Listen []string `json:"listen"`
}

// ModelConfig descreve o llama-server de um papel.
type ModelConfig struct {
	URL         string `json:"url"`                   // base do llama-server, ex.: http://127.0.0.1:8081
	Model       string `json:"model"`                 // nome informativo, aparece em logs e métricas
	GGUFSHA256  string `json:"gguf_sha256,omitempty"` // checksum do GGUF, conferido na Fase 1
	ContextSize int    `json:"context_size"`
}

// LoopConfig limita o loop ReAct.
type LoopConfig struct {
	MaxSteps       int      `json:"max_steps"`
	RequestTimeout Duration `json:"request_timeout"`
}

// StorageConfig aponta o arquivo SQLite.
type StorageConfig struct {
	Path string `json:"path"`
}

// LogConfig controla o slog.
type LogConfig struct {
	Level  string `json:"level"`          // debug, info, warn, error
	Format string `json:"format"`         // json ou text
	File   string `json:"file,omitempty"` // vazio = ~/.local/state/nexus/nexus.log; "stderr" = terminal
}

// Duration aceita "90s", "2m" no JSON.
type Duration struct{ time.Duration }

// UnmarshalJSON lê uma duração no formato de time.ParseDuration.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duração deve ser texto, ex.: \"90s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalJSON escreve a duração como texto.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Default devolve a configuração padrão. Caminhos vazios (storage, log) são
// resolvidos por Load a partir do HOME.
func Default() Config {
	return Config{
		Server: ServerConfig{Listen: []string{"127.0.0.1:8080"}},
		Models: map[domain.Role]ModelConfig{
			domain.RoleMain:  {URL: "http://127.0.0.1:8081", Model: "main-7b-q4", ContextSize: 8192},
			domain.RoleAux:   {URL: "http://127.0.0.1:8082", Model: "aux-3b-q4", ContextSize: 4096},
			domain.RoleEmbed: {URL: "http://127.0.0.1:8083", Model: "nomic-embed-text", ContextSize: 2048},
		},
		Loop:    LoopConfig{MaxSteps: 6, RequestTimeout: Duration{2 * time.Minute}},
		Storage: StorageConfig{}, // vazio = ~/.local/share/nexus/nexus.db, resolvido em Load
		Log:     LogConfig{Level: "info", Format: "json"},
	}
}

// Load monta a config. Sem path, procura sozinho: NEXUS_CONFIG, depois
// $XDG_CONFIG_HOME/nexus/config.json (ou ~/.config/nexus/config.json). Se o
// arquivo padrão não existe, valem os padrões; um arquivo pedido explicitamente
// (path ou NEXUS_CONFIG) que não existe é erro.
func Load(path string, env func(string) (string, bool)) (Config, error) {
	if env == nil {
		env = os.LookupEnv
	}
	explicit := path != ""
	if !explicit {
		if v, ok := env("NEXUS_CONFIG"); ok && v != "" {
			path, explicit = v, true
		} else {
			path = DefaultConfigPath(env)
		}
	}

	cfg := Default()
	if path != "" {
		f, err := os.Open(path)
		switch {
		case err == nil:
			defer f.Close()
			if err := decode(f, &cfg); err != nil {
				return Config{}, fmt.Errorf("config %s: %w", path, err)
			}
			cfg.Source = path
		case !explicit && errors.Is(err, fs.ErrNotExist):
			// sem arquivo no lugar padrão: os padrões bastam
		default:
			return Config{}, fmt.Errorf("config: %w", err)
		}
	}
	if err := applyEnv(&cfg, env); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if cfg.Log.File == "" {
		cfg.Log.File = DefaultLogPath(env)
	}
	if cfg.Storage.Path == "" {
		cfg.Storage.Path = DefaultDataPath(env)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("config inválida:\n%w", err)
	}
	return cfg, nil
}

// DefaultConfigPath é onde o Nexus procura a config; vazio se não há HOME.
func DefaultConfigPath(env func(string) (string, bool)) string {
	return xdgPath(env, "XDG_CONFIG_HOME", ".config", "config.json")
}

// DefaultDataPath é onde fica o SQLite por padrão; "nexus.db" no diretório
// atual se não há HOME.
func DefaultDataPath(env func(string) (string, bool)) string {
	if p := xdgPath(env, "XDG_DATA_HOME", filepath.Join(".local", "share"), "nexus.db"); p != "" {
		return p
	}
	return "nexus.db"
}

// DefaultLogPath é onde os logs vão por padrão; "stderr" se não há HOME.
func DefaultLogPath(env func(string) (string, bool)) string {
	if p := xdgPath(env, "XDG_STATE_HOME", filepath.Join(".local", "state"), "nexus.log"); p != "" {
		return p
	}
	return "stderr"
}

func xdgPath(env func(string) (string, bool), xdgVar, homeSub, file string) string {
	if v, ok := env(xdgVar); ok && filepath.IsAbs(v) {
		return filepath.Join(v, "nexus", file)
	}
	if home, ok := env("HOME"); ok && home != "" {
		return filepath.Join(home, homeSub, "nexus", file)
	}
	return ""
}

func decode(r io.Reader, cfg *Config) error {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	// encoding/json começa cada entrada de mapa do zero: um papel presente no
	// arquivo substitui o padrão por inteiro, sem herdar campos dele.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("conteúdo extra depois do objeto JSON")
	}
	return nil
}

// Variáveis de ambiente aceitas. Segredos, quando existirem, entram por aqui
// ou por arquivo 0600, nunca pelo repositório.
func applyEnv(cfg *Config, env func(string) (string, bool)) error {
	if v, ok := env("NEXUS_LISTEN"); ok {
		cfg.Server.Listen = splitList(v)
	}
	if v, ok := env("NEXUS_STORAGE_PATH"); ok {
		cfg.Storage.Path = v
	}
	if v, ok := env("NEXUS_LOG_LEVEL"); ok {
		cfg.Log.Level = v
	}
	if v, ok := env("NEXUS_LOG_FORMAT"); ok {
		cfg.Log.Format = v
	}
	if v, ok := env("NEXUS_LOG_FILE"); ok {
		cfg.Log.File = v
	}
	if v, ok := env("NEXUS_MAX_STEPS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("NEXUS_MAX_STEPS: %w", err)
		}
		cfg.Loop.MaxSteps = n
	}
	for _, role := range domain.Roles() {
		key := "NEXUS_MODEL_" + strings.ToUpper(string(role)) + "_URL"
		if v, ok := env(key); ok {
			m := cfg.Models[role]
			m.URL = v
			cfg.Models[role] = m
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate devolve todos os problemas de uma vez, juntos com errors.Join.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if len(c.Server.Listen) == 0 {
		add("server.listen: informe ao menos um endereço")
	}
	for _, addr := range c.Server.Listen {
		if err := checkListen(addr); err != nil {
			add("server.listen %q: %v", addr, err)
		}
	}

	for role := range c.Models {
		if !role.Valid() {
			add("models.%s: papel desconhecido (use main, aux ou embed)", role)
		}
	}
	for _, role := range domain.Roles() {
		m, ok := c.Models[role]
		if !ok {
			add("models.%s: papel obrigatório ausente", role)
			continue
		}
		if err := checkModelURL(m.URL); err != nil {
			add("models.%s.url %q: %v", role, m.URL, err)
		}
		if m.Model == "" {
			add("models.%s.model: obrigatório", role)
		}
		if m.ContextSize <= 0 {
			add("models.%s.context_size: deve ser maior que zero", role)
		}
		if m.GGUFSHA256 != "" && !sha256Hex.MatchString(m.GGUFSHA256) {
			add("models.%s.gguf_sha256: esperado SHA-256 em 64 caracteres hex minúsculos", role)
		}
	}

	if c.Loop.MaxSteps < 1 || c.Loop.MaxSteps > 50 {
		add("loop.max_steps: %d fora de 1..50", c.Loop.MaxSteps)
	}
	if c.Loop.RequestTimeout.Duration <= 0 {
		add("loop.request_timeout: deve ser maior que zero")
	}
	if strings.TrimSpace(c.Storage.Path) == "" {
		add("storage.path: obrigatório")
	}
	if _, err := c.Log.SlogLevel(); err != nil {
		add("log.level: %v", err)
	}
	if c.Log.Format != "json" && c.Log.Format != "text" {
		add("log.format %q: use json ou text", c.Log.Format)
	}
	return errors.Join(errs...)
}

func checkListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return errors.New("porta inválida")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("use um IP explícito (loopback ou o IP da interface WireGuard)")
	}
	if ip.IsUnspecified() {
		return errors.New("escutar em todas as interfaces não é permitido")
	}
	return nil
}

// O llama-server só escuta em 127.0.0.1; uma URL fora do loopback indica
// erro de configuração ou modelo exposto.
func checkModelURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("esquema deve ser http ou https")
	}
	if host := u.Hostname(); host != "localhost" {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.IsLoopback() {
			return errors.New("o llama-server deve ficar no loopback (127.0.0.1)")
		}
	}
	if u.Port() == "" {
		return errors.New("informe a porta")
	}
	return nil
}

// SlogLevel converte o nível textual.
func (l LogConfig) SlogLevel() (slog.Level, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(l.Level)); err != nil {
		return 0, fmt.Errorf("%q: use debug, info, warn ou error", l.Level)
	}
	return lv, nil
}

// Open abre o destino dos logs: o arquivo (criado com 0600, diretório 0700)
// ou o stderr. Fechar o stderr devolvido não faz nada.
func (l LogConfig) Open() (io.WriteCloser, error) {
	if l.File == "" || l.File == "stderr" {
		return nopCloser{os.Stderr}, nil
	}
	if err := os.MkdirAll(filepath.Dir(l.File), 0o700); err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	f, err := os.OpenFile(l.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	return f, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// NewLogger cria o logger do processo a partir da config.
func (l LogConfig) NewLogger(w io.Writer) (*slog.Logger, error) {
	lv, err := l.SlogLevel()
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lv}
	if l.Format == "text" {
		return slog.New(slog.NewTextHandler(w, opts)), nil
	}
	return slog.New(slog.NewJSONHandler(w, opts)), nil
}
