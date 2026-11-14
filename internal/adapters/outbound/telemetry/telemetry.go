// Package telemetry guarda as métricas em memória e as expõe em /metrics no
// formato texto do Prometheus. Implementa ports.Metrics, só com a stdlib.
//
// Convenção: nomes terminados em _total são contadores (somam o valor); os
// demais são resumos com _sum e _count, de onde sai a média.
package telemetry

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type series struct {
	labels string
	sum    float64
	count  uint64
}

// Registry é um ports.Metrics seguro para uso concorrente.
type Registry struct {
	mu   sync.Mutex
	data map[string]map[string]*series // nome -> labels -> série
}

// New cria um registro vazio.
func New() *Registry { return &Registry{data: map[string]map[string]*series{}} }

// Observe implementa ports.Metrics. labels vêm em pares: chave, valor.
func (r *Registry) Observe(name string, value float64, labels ...string) {
	key := formatLabels(labels)
	r.mu.Lock()
	defer r.mu.Unlock()
	byLabel, ok := r.data[name]
	if !ok {
		byLabel = map[string]*series{}
		r.data[name] = byLabel
	}
	s, ok := byLabel[key]
	if !ok {
		s = &series{labels: key}
		byLabel[key] = s
	}
	s.sum += value
	s.count++
}

func formatLabels(labels []string) string {
	if len(labels) < 2 {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, kv{sanitize(labels[i]), labels[i+1]})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + strconv.Quote(p.v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}

// Text devolve todas as métricas no formato de exposição do Prometheus.
func (r *Registry) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.data))
	for n := range r.data {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		metric := sanitize(name)
		byLabel := r.data[name]
		keys := make([]string, 0, len(byLabel))
		for k := range byLabel {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.HasSuffix(metric, "_total") {
			fmt.Fprintf(&b, "# TYPE %s counter\n", metric)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s%s %s\n", metric, k, num(byLabel[k].sum))
			}
			continue
		}
		fmt.Fprintf(&b, "# TYPE %s summary\n", metric)
		for _, k := range keys {
			s := byLabel[k]
			fmt.Fprintf(&b, "%s_sum%s %s\n", metric, k, num(s.sum))
			fmt.Fprintf(&b, "%s_count%s %d\n", metric, k, s.count)
		}
	}
	return b.String()
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// ServeHTTP expõe /metrics.
func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(r.Text()))
}
