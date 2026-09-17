package loadtest

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
)

// Scenario describes several weighted request definitions that get mixed
// together during a single run, e.g. "80% GET /users, 20% POST /users".
// Kept as JSON (stdlib encoding/json) rather than YAML so the whole
// project stays dependency-free; a YAML wrapper can be layered on top
// trivially if desired.
type Scenario struct {
	Name     string           `json:"name"`
	Requests []ScenarioRequest `json:"requests"`
}

type ScenarioRequest struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Weight  int               `json:"weight"` // relative weight, e.g. 8 vs 2
}

// LoadScenario reads and validates a scenario file.
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading scenario file: %w", err)
	}

	var s Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing scenario JSON: %w", err)
	}
	if len(s.Requests) == 0 {
		return nil, fmt.Errorf("scenario %q has no requests", path)
	}
	for i := range s.Requests {
		if s.Requests[i].Weight <= 0 {
			s.Requests[i].Weight = 1
		}
		if s.Requests[i].Method == "" {
			s.Requests[i].Method = http.MethodGet
		}
	}
	return &s, nil
}

// Picker returns a weighted-random ScenarioRequest chooser. Kept separate
// from the Runner so it can be unit-tested with a fixed seed.
type Picker struct {
	cumulative []int
	requests   []ScenarioRequest
	total      int
	rng        *rand.Rand
}

func NewPicker(s *Scenario, seed int64) *Picker {
	p := &Picker{rng: rand.New(rand.NewSource(seed))}
	sum := 0
	for _, r := range s.Requests {
		sum += r.Weight
		p.cumulative = append(p.cumulative, sum)
		p.requests = append(p.requests, r)
	}
	p.total = sum
	return p
}

func (p *Picker) Pick() ScenarioRequest {
	n := p.rng.Intn(p.total) + 1
	for i, c := range p.cumulative {
		if n <= c {
			return p.requests[i]
		}
	}
	return p.requests[len(p.requests)-1]
}

// ToHeader converts a plain map into an http.Header.
func ToHeader(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}
