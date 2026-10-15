package config

import (
	"errors"
	"math"
	"net/url"
)

const DefaultOTelSampleRatio = 0.1

const (
	otelHTTP  = "http"
	otelHTTPS = "https"
)

// OTel configures optional OTLP/HTTP export. Prometheus scrape remains independent.
type OTel struct {
	Enabled          bool    `yaml:"enabled"`
	Endpoint         string  `yaml:"endpoint"`
	ServiceName      string  `yaml:"service_name"`
	SampleRatio      float64 `yaml:"sample_ratio"`
	TimeoutMS        int     `yaml:"timeout_ms"`
	ExportIntervalMS int     `yaml:"export_interval_ms"`
	MaxQueue         int     `yaml:"max_queue"`
	MaxBatch         int     `yaml:"max_batch"`
}

func (c *OTel) ApplyDefaults() {
	if c.Endpoint == "" {
		c.Endpoint = "http://alloy:4318"
	}
	if c.ServiceName == "" {
		c.ServiceName = "loom"
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = 1000
	}
	if c.ExportIntervalMS == 0 {
		c.ExportIntervalMS = 15000
	}
	if c.MaxQueue == 0 {
		c.MaxQueue = 512
	}
	if c.MaxBatch == 0 {
		c.MaxBatch = 64
	}
}
func (c *OTel) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != otelHTTP && u.Scheme != otelHTTPS) || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return errors.New("otel endpoint must be an HTTP(S) collector origin without credentials, path, or query")
	}
	if len(c.ServiceName) == 0 || len(c.ServiceName) > 64 || math.IsNaN(c.SampleRatio) ||
		math.IsInf(c.SampleRatio, 0) ||
		c.SampleRatio < 0 ||
		c.SampleRatio > 1 ||
		c.TimeoutMS < 1 ||
		c.TimeoutMS > 2000 ||
		c.ExportIntervalMS < 1000 ||
		c.ExportIntervalMS > 300000 ||
		c.MaxQueue < 1 ||
		c.MaxQueue > 4096 ||
		c.MaxBatch < 1 ||
		c.MaxBatch > c.MaxQueue {
		return errors.New("invalid otel service name, sampling ratio, or exporter bounds")
	}
	return nil
}
