package telemetry

import (
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestTextFormat(t *testing.T) {
	r := New()
	r.Observe("nexus_tool_calls_total", 1, "tool", "read_file", "decision", "allowed")
	r.Observe("nexus_tool_calls_total", 1, "tool", "read_file", "decision", "allowed")
	r.Observe("nexus_tool_calls_total", 1, "tool", "memory_save", "decision", "denied_user")
	r.Observe("nexus_time_to_first_token_seconds", 0.5, "role", "main")
	r.Observe("nexus_time_to_first_token_seconds", 1.5, "role", "main")
	r.Observe("nexus_react_steps", 2)
	r.Observe("nexus_odd-name_total", 1, "a\"b", "x\"y", "sozinho")

	want := `# TYPE nexus_odd_name_total counter
nexus_odd_name_total{a_b="x\"y"} 1
# TYPE nexus_react_steps summary
nexus_react_steps_sum 2
nexus_react_steps_count 1
# TYPE nexus_time_to_first_token_seconds summary
nexus_time_to_first_token_seconds_sum{role="main"} 2
nexus_time_to_first_token_seconds_count{role="main"} 2
# TYPE nexus_tool_calls_total counter
nexus_tool_calls_total{decision="allowed",tool="read_file"} 2
nexus_tool_calls_total{decision="denied_user",tool="memory_save"} 1
`
	if got := r.Text(); got != want {
		t.Errorf("texto:\n%s\nquer:\n%s", got, want)
	}
}

func TestHandlerAndConcurrency(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Observe("x_total", 1) }()
	}
	wg.Wait()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "x_total 50") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("handler: %s %q", rec.Header(), body)
	}
}
