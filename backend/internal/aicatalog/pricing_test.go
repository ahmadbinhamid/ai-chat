package aicatalog

import (
	"math"
	"testing"
	"time"
)

func flashPricing() *Pricing {
	return &Pricing{Input: 0.15, CacheHitInput: 0.003, Output: 0.6, Peak: &PeakPricing{
		Input: 0.3, CacheHitInput: 0.006, Output: 1.2, HoursUTC: []string{"01:00-04:00", "06:00-10:00"}, WeekdaysOnly: true,
	}}
}

func TestPricing_Cost(t *testing.T) {
	// Friday 2026-10-09; Saturday 2026-10-10.
	at := func(day, hh, mm int) time.Time { return time.Date(2026, 10, day, hh, mm, 0, 0, time.UTC) }
	tests := []struct {
		name string
		at   time.Time
		want float64
	}{
		{"off-peak weekday", at(9, 12, 0), (1_000_000*0.15 + 2_000_000*0.003 + 500_000*0.6) / 1_000_000},
		{"peak weekday morning", at(9, 7, 30), (1_000_000*0.3 + 2_000_000*0.006 + 500_000*1.2) / 1_000_000},
		{"peak window start is inclusive", at(9, 1, 0), (1_000_000*0.3 + 2_000_000*0.006 + 500_000*1.2) / 1_000_000},
		{"peak window end is exclusive", at(9, 4, 0), (1_000_000*0.15 + 2_000_000*0.003 + 500_000*0.6) / 1_000_000},
		{"weekend in peak hours is off-peak", at(10, 7, 30), (1_000_000*0.15 + 2_000_000*0.003 + 500_000*0.6) / 1_000_000},
		{"non-UTC time is converted", time.Date(2026, 10, 9, 12, 30, 0, 0, time.FixedZone("PKT", 5*3600)), (1_000_000*0.3 + 2_000_000*0.006 + 500_000*1.2) / 1_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flashPricing().Cost(1_000_000, 2_000_000, 500_000, tt.at); math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("Cost = %v, want %v", got, tt.want)
			}
		})
	}
	noPeak := &Pricing{Input: 1, CacheHitInput: 0.5, Output: 2}
	if got := noPeak.Cost(1_000_000, 0, 0, time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)); got != 1 {
		t.Fatalf("without peak pricing, Cost = %v, want the base rate", got)
	}
}

func TestPricing_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(p *Pricing)
		wantErr bool
	}{
		{"valid", func(*Pricing) {}, false},
		{"negative base price", func(p *Pricing) { p.Output = -1 }, true},
		{"negative peak price", func(p *Pricing) { p.Peak.Input = -0.1 }, true},
		{"peak without hours", func(p *Pricing) { p.Peak.HoursUTC = nil }, true},
		{"malformed window", func(p *Pricing) { p.Peak.HoursUTC = []string{"1am-4am"} }, true},
		{"window ending before it starts", func(p *Pricing) { p.Peak.HoursUTC = []string{"10:00-06:00"} }, true},
		{"no peak at all", func(p *Pricing) { p.Peak = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := flashPricing()
			tt.mutate(p)
			if err := p.validate(); (err != nil) != tt.wantErr {
				t.Fatalf("validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParse_RejectsInvalidPricing(t *testing.T) {
	m := validCatalogue(t)
	model(m, 0)["pricing"] = map[string]any{"input": -1, "cache_hit_input": 0, "output": 1}
	if _, err := Parse(encode(t, m), withKey); err == nil {
		t.Fatal("want a negative price rejected at load")
	}
}
