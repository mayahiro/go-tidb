package runtimecapture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

// ServerRUBaselineVersion identifies the ServerRU baseline JSON format.
const ServerRUBaselineVersion = 1

// ServerRUBaseline stores deterministic per-fingerprint ServerRU statistics
// for later offline comparison.
type ServerRUBaseline struct {
	Version               int                           `json:"version"`
	ServerRUByFingerprint []FingerprintServerRUBaseline `json:"server_ru_by_fingerprint"`
}

// FingerprintServerRUBaseline stores successful ServerRU measurements and
// their capture coverage for one bind-free fingerprint.
type FingerprintServerRUBaseline struct {
	Fingerprint string  `json:"fingerprint"`
	Count       int     `json:"count"`
	Samples     int     `json:"samples"`
	Total       float64 `json:"total"`
	Mean        float64 `json:"mean"`
	Minimum     float64 `json:"min"`
	Maximum     float64 `json:"max"`
}

// NewServerRUBaseline creates a comparison-ready baseline from a completed
// runtime analysis. It rejects incomplete measurement coverage, fewer than
// five samples per fingerprint, and any collection error.
func NewServerRUBaseline(analysis Analysis) (ServerRUBaseline, error) {
	statistics := make([]FingerprintServerRUBaseline, len(analysis.ServerRUByFingerprint))
	for index, source := range analysis.ServerRUByFingerprint {
		if source.Errors != 0 {
			return ServerRUBaseline{}, fmt.Errorf(
				"create ServerRU baseline: server_ru_by_fingerprint[%d] has ServerRU collection errors: %d",
				index,
				source.Errors,
			)
		}
		statistics[index] = FingerprintServerRUBaseline{
			Fingerprint: source.Fingerprint,
			Count:       source.Count,
			Samples:     source.Samples,
			Total:       source.Total,
			Mean:        source.Mean,
			Minimum:     source.Minimum,
			Maximum:     source.Maximum,
		}
	}
	baseline := ServerRUBaseline{
		Version:               ServerRUBaselineVersion,
		ServerRUByFingerprint: statistics,
	}
	if err := baseline.Validate(); err != nil {
		return ServerRUBaseline{}, fmt.Errorf("create ServerRU baseline: %w", err)
	}
	return baseline, nil
}

// Validate checks the version, ordering, comparison coverage, and numeric
// invariants required for a ServerRU baseline.
func (baseline ServerRUBaseline) Validate() error {
	if baseline.Version != ServerRUBaselineVersion {
		return fmt.Errorf("ServerRU baseline version is %d, want %d", baseline.Version, ServerRUBaselineVersion)
	}
	if len(baseline.ServerRUByFingerprint) == 0 {
		return fmt.Errorf(
			"ServerRU baseline requires at least one measured fingerprint with at least %d successful samples",
			ServerRUComparisonMinimumSamples,
		)
	}
	for index, statistics := range baseline.ServerRUByFingerprint {
		if statistics.Fingerprint == "" {
			return fmt.Errorf("server_ru_by_fingerprint[%d] requires fingerprint", index)
		}
		if index > 0 && statistics.Fingerprint <= baseline.ServerRUByFingerprint[index-1].Fingerprint {
			return fmt.Errorf("server_ru_by_fingerprint[%d] fingerprint must be unique and sorted", index)
		}
		if statistics.Count < 1 {
			return fmt.Errorf("server_ru_by_fingerprint[%d] requires a positive count", index)
		}
		if statistics.Samples < 1 || statistics.Samples > statistics.Count {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has invalid sample count", index)
		}
		values := []struct {
			name  string
			value float64
		}{
			{name: "total", value: statistics.Total},
			{name: "mean", value: statistics.Mean},
			{name: "min", value: statistics.Minimum},
			{name: "max", value: statistics.Maximum},
		}
		for _, value := range values {
			if value.value < 0 || math.IsNaN(value.value) || math.IsInf(value.value, 0) {
				return fmt.Errorf("server_ru_by_fingerprint[%d] has invalid %s", index, value.name)
			}
		}
		if statistics.Minimum > statistics.Mean || statistics.Mean > statistics.Maximum {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has inconsistent min, mean, and max", index)
		}
		expectedMean := statistics.Total / float64(statistics.Samples)
		tolerance := math.Max(1, math.Abs(expectedMean)) * 1e-12
		if statistics.Total == math.MaxFloat64 && statistics.Mean < expectedMean-tolerance {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has inconsistent total, sample count, and mean", index)
		}
		if statistics.Total != math.MaxFloat64 && math.Abs(statistics.Mean-expectedMean) > tolerance {
			return fmt.Errorf("server_ru_by_fingerprint[%d] has inconsistent total, sample count, and mean", index)
		}
		if statistics.Samples != statistics.Count {
			return fmt.Errorf("server_ru_by_fingerprint[%d] requires complete measurement coverage", index)
		}
		if statistics.Samples < ServerRUComparisonMinimumSamples {
			return fmt.Errorf(
				"server_ru_by_fingerprint[%d] requires at least %d samples",
				index,
				ServerRUComparisonMinimumSamples,
			)
		}
	}
	return nil
}

// EncodeServerRUBaseline writes one validated baseline JSON value followed by
// a newline.
func EncodeServerRUBaseline(writer io.Writer, baseline ServerRUBaseline) error {
	if writer == nil {
		return fmt.Errorf("ServerRU baseline output is nil")
	}
	if err := baseline.Validate(); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(baseline)
}

// DecodeServerRUBaseline reads exactly one strictly validated baseline JSON
// value.
func DecodeServerRUBaseline(reader io.Reader) (ServerRUBaseline, error) {
	if reader == nil {
		return ServerRUBaseline{}, fmt.Errorf("ServerRU baseline input is nil")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var baseline ServerRUBaseline
	if err := decoder.Decode(&baseline); err != nil {
		return ServerRUBaseline{}, fmt.Errorf("decode ServerRU baseline: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ServerRUBaseline{}, fmt.Errorf("ServerRU baseline contains more than one JSON value")
		}
		return ServerRUBaseline{}, fmt.Errorf("decode trailing ServerRU baseline data: %w", err)
	}
	if err := baseline.Validate(); err != nil {
		return ServerRUBaseline{}, fmt.Errorf("validate ServerRU baseline: %w", err)
	}
	return baseline, nil
}
