package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"
)

// VINs start with this prefix so mock vehicles are easy to find and remove.
const vinPrefix = "FEED"

// vinChars is the VIN alphabet, which excludes I, O and Q.
const vinChars = "ABCDEFGHJKLMNPRSTUVWXYZ0123456789"

// Odds for each vehicle in a batch, once earlier vehicles exist to change.
const (
	percentPriceChange = 30
	percentLeftStock   = 10
)

type model struct {
	name string
	base int64 // list price in dollars
}

var catalog = []struct {
	make   string
	models []model
}{
	{"Toyota", []model{{"Corolla", 22000}, {"Camry", 28000}, {"RAV4", 31000}, {"Highlander", 40000}, {"Tacoma", 33000}}},
	{"Honda", []model{{"Civic", 24000}, {"Accord", 29000}, {"CR-V", 32000}, {"Pilot", 41000}}},
	{"Ford", []model{{"F-150", 45000}, {"Escape", 29000}, {"Explorer", 39000}, {"Mustang", 35000}}},
	{"Chevrolet", []model{{"Silverado", 44000}, {"Equinox", 28000}, {"Malibu", 25000}}},
	{"Nissan", []model{{"Altima", 26000}, {"Rogue", 29000}, {"Sentra", 21000}}},
	{"Hyundai", []model{{"Elantra", 22000}, {"Tucson", 29000}, {"Santa Fe", 33000}}},
	{"Kia", []model{{"Forte", 21000}, {"Sportage", 28000}, {"Telluride", 38000}}},
	{"Subaru", []model{{"Forester", 29000}, {"Outback", 31000}}},
}

// Vehicle is one item of an ingest batch. Status is always sent, because the
// API sets a vehicle to IN_STOCK when it is left out.
type Vehicle struct {
	VIN        string      `json:"vin"`
	Make       string      `json:"make"`
	Model      string      `json:"model"`
	Year       int         `json:"year"`
	Price      json.Number `json:"price"`
	ReceivedAt string      `json:"received_at"`
	Status     string      `json:"status"`

	priceCents int64
}

func (v *Vehicle) setPrice(cents int64) {
	v.priceCents = cents
	v.Price = json.Number(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}

// Batch is a generated batch and what it contains.
type Batch struct {
	Vehicles []Vehicle
	New      int
	Repriced int
	Left     int // vehicles marked SOLD or REMOVED
}

// Generator produces random batches. It remembers the in-stock vehicles it has
// successfully sent, so later batches can change them.
type Generator struct {
	rng      *rand.Rand
	now      func() time.Time
	maxBatch int
	known    []Vehicle
}

// NewGenerator returns a generator of batches holding 1 to maxBatch vehicles.
func NewGenerator(seed uint64, maxBatch int, now func() time.Time) *Generator {
	return &Generator{
		rng:      rand.New(rand.NewPCG(seed, seed)), //nolint:gosec // mock data, not security-sensitive
		now:      now,
		maxBatch: maxBatch,
	}
}

// Next builds a batch without changing what the generator remembers. Call
// Commit once the API has accepted it.
func (g *Generator) Next() Batch {
	size := 1 + g.rng.IntN(g.maxBatch)
	// A vehicle may only appear once per batch, so walk the known ones in a
	// random order instead of picking independently.
	order := g.rng.Perm(len(g.known))
	var b Batch
	for range size {
		roll := g.rng.IntN(100)
		if roll >= percentPriceChange+percentLeftStock || len(order) == 0 {
			b.Vehicles = append(b.Vehicles, g.newVehicle())
			b.New++
			continue
		}
		v := g.known[order[0]]
		order = order[1:]
		if roll < percentPriceChange {
			g.reprice(&v)
			b.Repriced++
		} else {
			v.Status = "SOLD"
			if g.rng.IntN(5) == 0 {
				v.Status = "REMOVED"
			}
			b.Left++
		}
		b.Vehicles = append(b.Vehicles, v)
	}
	return b
}

// Commit records an accepted batch.
func (g *Generator) Commit(b Batch) {
	sent := make(map[string]Vehicle, len(b.Vehicles))
	for _, v := range b.Vehicles {
		sent[v.VIN] = v
	}
	kept := g.known[:0]
	for _, v := range g.known {
		if updated, ok := sent[v.VIN]; ok {
			delete(sent, v.VIN)
			if updated.Status != "IN_STOCK" {
				continue
			}
			v = updated
		}
		kept = append(kept, v)
	}
	g.known = kept
	for _, v := range b.Vehicles { // in batch order, so a seed stays reproducible
		if _, isNew := sent[v.VIN]; isNew {
			g.known = append(g.known, v)
		}
	}
}

func (g *Generator) newVehicle() Vehicle {
	today := g.now().UTC()
	c := catalog[g.rng.IntN(len(catalog))]
	m := c.models[g.rng.IntN(len(c.models))]
	yearsOld := g.rng.IntN(4)
	dollars := float64(m.base) * (1 - 0.07*float64(yearsOld)) * (0.95 + 0.15*g.rng.Float64())

	// Mostly fresh arrivals, plus some older stock so aging figures move too.
	age := g.rng.IntN(8)
	if g.rng.IntN(100) < 20 {
		age = 91 + g.rng.IntN(90)
	}
	vin := []byte(vinPrefix)
	for len(vin) < 17 {
		vin = append(vin, vinChars[g.rng.IntN(len(vinChars))])
	}
	v := Vehicle{
		VIN:        string(vin),
		Make:       c.make,
		Model:      m.name,
		Year:       today.Year() - yearsOld,
		ReceivedAt: today.AddDate(0, 0, -age).Format(time.DateOnly),
		Status:     "IN_STOCK",
	}
	v.setPrice(int64(dollars/100)*10000 - 100) // priced like 24,899.00
	return v
}

// reprice always changes the price, so the API counts the row as updated.
func (g *Generator) reprice(v *Vehicle) {
	drop := (2 + g.rng.Int64N(14)) * 10000 // 200 to 1,500 dollars
	if v.priceCents-drop < 100000 {
		v.setPrice(v.priceCents + drop)
		return
	}
	v.setPrice(v.priceCents - drop)
}
