package config

import (
	"errors"
	"net/url"
	"path"
)

const (
	defaultTranscriptionModel = "gpt-transcribe"
	openAIProvider            = "openai"
)

// FileTools names a private processor with no network or user credential access.
type FileTools struct {
	Enabled   bool   `yaml:"enabled"`
	Socket    string `yaml:"socket"`
	TimeoutMS int    `yaml:"timeout_ms"`
}

// Transcription is deployment policy; tariffs are snapshotted before dispatch.
type Transcription struct {
	Enabled            bool   `yaml:"enabled"`
	Provider           string `yaml:"provider"`
	Model              string `yaml:"model"`
	Endpoint           string `yaml:"endpoint"`
	APIKey             string `yaml:"api_key"`
	CreditsPerSecond   int64  `yaml:"credits_per_second"`
	MaxDurationSeconds int    `yaml:"max_duration_seconds"`
}

func (c Config) ValidateFileTools() error {
	if c.FileTools.Enabled &&
		(!path.IsAbs(c.FileTools.Socket) || c.FileTools.TimeoutMS < 1 || c.FileTools.TimeoutMS > 30000) {
		return errors.New("invalid file processor configuration")
	}
	if !c.Transcription.Enabled {
		return nil
	}
	if err := c.Transcription.validateEndpoint(); err != nil {
		return err
	}
	if !c.FileTools.Enabled || !c.Transcription.validCredentials() || !c.Transcription.validBilling() {
		return errors.New("invalid transcription configuration")
	}
	return nil
}
func (c Transcription) validateEndpoint() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "https" {
		return errors.New("invalid transcription endpoint")
	}
	return nil
}
func (c Transcription) validCredentials() bool {
	return c.Provider == openAIProvider && (c.Model == defaultTranscriptionModel || c.Model == "whisper-1") &&
		c.APIKey != ""
}
func (c Transcription) validBilling() bool {
	return c.CreditsPerSecond > 0 && c.CreditsPerSecond <= 1000000000 && c.MaxDurationSeconds >= 1 &&
		c.MaxDurationSeconds <= 300
}

func applyFileDefaults(c *Config) {
	if c.FileTools.Socket == "" {
		c.FileTools.Socket = "/run/loom-files/processor.sock"
	}
	if c.FileTools.TimeoutMS == 0 {
		c.FileTools.TimeoutMS = 30000
	}
	if c.Transcription.Provider == "" {
		c.Transcription.Provider = openAIProvider
	}
	if c.Transcription.Model == "" {
		c.Transcription.Model = defaultTranscriptionModel
	}
	if c.Transcription.Endpoint == "" {
		c.Transcription.Endpoint = "https://api.openai.com/v1/audio/transcriptions"
	}
	if c.Transcription.MaxDurationSeconds == 0 {
		c.Transcription.MaxDurationSeconds = 300
	}
	if c.Transcription.CreditsPerSecond == 0 && c.Transcription.Model == defaultTranscriptionModel {
		c.Transcription.CreditsPerSecond = 75
	}
	if c.Transcription.APIKey == "" {
		c.Transcription.APIKey = c.Providers[openAIProvider].APIKey
	}
}
