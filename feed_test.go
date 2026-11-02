package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func clock() time.Time { return fixedNow }

// Every generated vehicle must satisfy the API's ingest rules, across many
// batches that also contain repriced and sold vehicles.
func TestGeneratedVehiclesFollowIngestRules(t *testing.T) {
	vin := regexp.MustCompile(`^FEED[A-HJ-NPR-Z0-9]{13}$`)
	price := regexp.MustCompile(`^[0-9]{1,10}\.[0-9]{2}$`)
	g := NewGenerator(1, 50, clock)
	var totals Batch
	for range 200 {
		b := g.Next()
		if n := len(b.Vehicles); n < 1 || n > 50 {
			t.Fatalf("batch size %d", n)
		}
		if b.New+b.Repriced+b.Left != len(b.Vehicles) {
			t.Fatalf("counts %d+%d+%d != %d", b.New, b.Repriced, b.Left, len(b.Vehicles))
		}
		seen := map[string]bool{}
		for _, v := range b.Vehicles {
			if seen[v.VIN] {
				t.Fatalf("duplicate VIN %s in one batch", v.VIN)
			}
			seen[v.VIN] = true
			if !vin.MatchString(v.VIN) {
				t.Errorf("bad VIN %q", v.VIN)
			}
			if v.Make == "" || v.Model == "" || v.Year < 1900 || v.Year > fixedNow.Year()+2 {
				t.Errorf("bad vehicle %+v", v)
			}
			if !price.MatchString(v.Price.String()) || v.priceCents <= 0 {
				t.Errorf("bad price %q", v.Price)
			}
			received, err := time.Parse(time.DateOnly, v.ReceivedAt)
			if err != nil || received.After(fixedNow) {
				t.Errorf("bad received_at %q", v.ReceivedAt)
			}
			if v.Status != "IN_STOCK" && v.Status != "SOLD" && v.Status != "REMOVED" {
				t.Errorf("bad status %q", v.Status)
			}
		}
		totals.New += b.New
		totals.Repriced += b.Repriced
		totals.Left += b.Left
		g.Commit(b)
	}
	if totals.New == 0 || totals.Repriced == 0 || totals.Left == 0 {
		t.Fatalf("expected a mix, got %+v", totals)
	}
}

func TestCommitTracksInStockVehicles(t *testing.T) {
	g := NewGenerator(2, 30, clock)
	first := g.Next()
	if first.New != len(first.Vehicles) {
		t.Fatalf("first batch must be all new, got %+v", first)
	}
	// A batch that was not accepted must not be remembered.
	if g.Next(); len(g.known) != 0 {
		t.Fatalf("Next changed state: %d known", len(g.known))
	}
	g.Commit(first)
	if len(g.known) != len(first.Vehicles) {
		t.Fatalf("known = %d, want %d", len(g.known), len(first.Vehicles))
	}

	sold := g.known[0]
	sold.Status = "SOLD"
	repriced := g.known[1]
	g.reprice(&repriced)
	g.Commit(Batch{Vehicles: []Vehicle{sold, repriced}})
	if len(g.known) != len(first.Vehicles)-1 {
		t.Fatalf("known = %d after a sale", len(g.known))
	}
	for _, v := range g.known {
		if v.VIN == sold.VIN {
			t.Fatal("sold vehicle is still known")
		}
		if v.VIN == repriced.VIN && v.Price != repriced.Price {
			t.Fatalf("price not updated: %s", v.Price)
		}
	}
}

func TestSameSeedRepeatsBatches(t *testing.T) {
	a, b := NewGenerator(9, 20, clock), NewGenerator(9, 20, clock)
	for range 20 {
		ba, bb := a.Next(), b.Next()
		ja, _ := json.Marshal(ba.Vehicles)
		jb, _ := json.Marshal(bb.Vehicles)
		if string(ja) != string(jb) {
			t.Fatalf("batches differ:\n%s\n%s", ja, jb)
		}
		a.Commit(ba)
		b.Commit(bb)
	}
}

func TestMintTokenIsSignedForIngest(t *testing.T) {
	tok, err := mintToken("secret-0123456789", "mock-feed", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	mac := hmac.New(sha256.New, []byte("secret-0123456789"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)); parts[2] != want {
		t.Fatal("bad signature")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Sub  string `json:"sub"`
		Role string `json:"role"`
		Exp  int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "mock-feed" || claims.Role != "ingest" || claims.Exp <= fixedNow.Unix() {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestClientPost(t *testing.T) {
	const errBody = `{"error":{"code":"%s","message":"nope","request_id":"r1"}}`
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
		isAuth  bool
	}{
		{"accepted", 200, `{"inserted":2,"updated":1}`, "", false},
		{"validation", 400, strings.Replace(errBody, "%s", "VALIDATION_ERROR", 1), "VALIDATION_ERROR: nope", false},
		{"rate limited", 429, strings.Replace(errBody, "%s", "RATE_LIMITED", 1), "RATE_LIMITED", false},
		{"not our error shape", 502, `bad gateway`, "status 502", false},
		{"unauthorized", 401, strings.Replace(errBody, "%s", "UNAUTHORIZED", 1), "UNAUTHORIZED", true},
		{"forbidden", 403, strings.Replace(errBody, "%s", "FORBIDDEN", 1), "FORBIDDEN", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/ingest/vehicles" || r.Header.Get("Authorization") != "Bearer tok" {
					t.Errorf("unexpected request %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				var body struct {
					Vehicles []Vehicle `json:"vehicles"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Vehicles) != 1 {
					t.Errorf("bad body: %v", err)
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			client := &Client{
				BaseURL: srv.URL,
				HTTP:    srv.Client(),
				Token:   func() (string, error) { return "tok", nil },
			}
			res, err := client.Post(context.Background(), []Vehicle{{VIN: "x"}})
			if c.wantErr == "" {
				if err != nil || res != (Result{Inserted: 2, Updated: 1}) {
					t.Fatalf("res = %+v, err = %v", res, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want %q", err, c.wantErr)
			}
			if errors.Is(err, errAuth) != c.isAuth {
				t.Fatalf("errors.Is(errAuth) = %v, want %v", !c.isAuth, c.isAuth)
			}
		})
	}
}
