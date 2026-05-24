package api

import (
	"context"
	"math"
	"time"

	"traefik-cloudflare-manager/lib"
)

type statsSample struct {
	At       time.Time `json:"at"`
	CPU      float64   `json:"cpu_percent"`
	Memory   uint64    `json:"memory_bytes"`
	MemLimit uint64    `json:"memory_limit_bytes"`
	RX       uint64    `json:"network_rx_bytes"`
	TX       uint64    `json:"network_tx_bytes"`
	RXRate   float64   `json:"network_rx_bytes_per_sec"`
	TXRate   float64   `json:"network_tx_bytes_per_sec"`
}

type statsPayload struct {
	Available       bool          `json:"available"`
	CPU             float64       `json:"cpu_percent"`
	Memory          uint64        `json:"memory_bytes"`
	MemLimit        uint64        `json:"memory_limit_bytes"`
	NetRX           uint64        `json:"network_rx_bytes"`
	NetTX           uint64        `json:"network_tx_bytes"`
	NetRXRate       float64       `json:"network_rx_bytes_per_sec"`
	NetTXRate       float64       `json:"network_tx_bytes_per_sec"`
	MemoryHuman     string        `json:"memory_human"`
	MemLimitHuman   string        `json:"memory_limit_human"`
	NetRXHuman      string        `json:"network_rx_human"`
	NetTXHuman      string        `json:"network_tx_human"`
	NetRXRateHuman  string        `json:"network_rx_rate_human"`
	NetTXRateHuman  string        `json:"network_tx_rate_human"`
	Error           string        `json:"error,omitempty"`
	History         []statsSample `json:"history"`
	HistoryWindowMS int64         `json:"history_window_ms"`
}

func (a *App) sampleStats(ctx context.Context) statsPayload {
	stats := a.docker.TraefikStats(ctx)
	now := time.Now().UTC()
	a.statsMu.Lock()
	defer a.statsMu.Unlock()

	if !stats.Available {
		a.statsHist = nil
		return statsPayload{Available: false, Error: stats.Error, HistoryWindowMS: int64((60 * time.Second) / time.Millisecond)}
	}

	sample := statsSample{
		At:       now,
		CPU:      stats.CPU,
		Memory:   stats.Memory,
		MemLimit: stats.MemLimit,
		RX:       stats.NetRX,
		TX:       stats.NetTX,
	}
	if len(a.statsHist) > 0 {
		prev := a.statsHist[len(a.statsHist)-1]
		elapsed := now.Sub(prev.At).Seconds()
		if elapsed > 0 {
			if sample.RX >= prev.RX {
				sample.RXRate = math.Round((float64(sample.RX-prev.RX)/elapsed)*10) / 10
			}
			if sample.TX >= prev.TX {
				sample.TXRate = math.Round((float64(sample.TX-prev.TX)/elapsed)*10) / 10
			}
		}
	}

	a.statsHist = append(a.statsHist, sample)
	cutoff := now.Add(-60 * time.Second)
	keep := 0
	for ; keep < len(a.statsHist); keep++ {
		if a.statsHist[keep].At.After(cutoff) {
			break
		}
	}
	if keep > 0 {
		a.statsHist = append([]statsSample(nil), a.statsHist[keep:]...)
	}
	history := append([]statsSample(nil), a.statsHist...)

	return statsPayload{
		Available:       true,
		CPU:             sample.CPU,
		Memory:          sample.Memory,
		MemLimit:        sample.MemLimit,
		NetRX:           sample.RX,
		NetTX:           sample.TX,
		NetRXRate:       sample.RXRate,
		NetTXRate:       sample.TXRate,
		MemoryHuman:     lib.FormatBytes(sample.Memory),
		MemLimitHuman:   lib.FormatBytes(sample.MemLimit),
		NetRXHuman:      lib.FormatBytes(sample.RX),
		NetTXHuman:      lib.FormatBytes(sample.TX),
		NetRXRateHuman:  lib.FormatBytes(uint64(sample.RXRate)) + "/s",
		NetTXRateHuman:  lib.FormatBytes(uint64(sample.TXRate)) + "/s",
		History:         history,
		HistoryWindowMS: int64((60 * time.Second) / time.Millisecond),
	}
}
