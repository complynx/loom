package config

import (
	"bytes"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	defaultPermissionsCacheTTLSeconds = 120
	maxShutdownDeadlineMS             = 5000
	heartbeatLeaseFactor              = 2
	maxJWKSCacheTTLSeconds            = 3600
	encryptionKeyBytes                = 32
	defaultSponsoredAttemptLimit      = 2
	maxSponsoredAttemptLimit          = 8
)

type HTTP struct {
	Listen            string `yaml:"listen"`
	MaxRequestBytes   int64  `yaml:"max_request_bytes"`
	MaxWaitMS         int    `yaml:"max_wait_ms"`
	SSEBufferEvents   int    `yaml:"sse_buffer_events"`
	DisconnectGraceMS int    `yaml:"disconnect_grace_ms"`
}
type Database struct {
	MigrateOnStart     bool   `yaml:"migrate_on_start"`
	DSN                string `yaml:"dsn"`
	MaxConnections     int32  `yaml:"max_connections"`
	StatementTimeoutMS int    `yaml:"statement_timeout_ms"`
	LockTimeoutMS      int    `yaml:"lock_timeout_ms"`
}
type Identity struct {
	AdminOrganizationID        string              `yaml:"admin_organization_id"`
	Issuer                     string              `yaml:"issuer"`
	Audience                   string              `yaml:"audience"`
	ClientID                   string              `yaml:"client_id"`
	ClientSecret               string              `yaml:"client_secret"`
	ClientKeyBase64            string              `yaml:"client_key_base64"             json:"-"`
	ClientKeyFile              string              `yaml:"client_key_file"`
	ClientKeyID                string              `yaml:"-"                             json:"-"`
	ClientPrivateKey           *rsa.PrivateKey     `yaml:"-"                             json:"-"`
	RefreshableExchangeEnabled bool                `yaml:"refreshable_exchange_enabled"`
	EncryptionKeyFile          string              `yaml:"encryption_key_file"`
	EncryptionKeyID            string              `yaml:"encryption_key_id"`
	PreviousEncryptionKeys     map[string]string   `yaml:"previous_encryption_keys"`
	PermissionsCacheTTLSeconds int                 `yaml:"permissions_cache_ttl_seconds"`
	JWKSCacheTTLSeconds        int                 `yaml:"jwks_cache_ttl_seconds"`
	RolePermissions            map[string][]string `yaml:"role_permissions"`
	ServiceSubjects            []string            `yaml:"service_subjects"`
	ServiceAccounts            map[string][]string `yaml:"service_accounts"`
}
type Discovery struct {
	DockerAPI                  string   `yaml:"docker_api"`
	Label                      string   `yaml:"label"`
	AllowedNetworks            []string `yaml:"allowed_networks"`
	PollSeconds                int      `yaml:"poll_seconds"`
	FreshnessTTLSeconds        int      `yaml:"freshness_ttl_seconds"`
	MaxManifestBytes           int64    `yaml:"max_manifest_bytes"`
	RequestTimeoutMS           int      `yaml:"request_timeout_ms"`
	DynamicCacheCeilingSeconds int      `yaml:"dynamic_cache_ceiling_seconds"`
}
type Provider struct {
	Kind       string `yaml:"kind"`
	Mailbox    string `yaml:"mailbox"`
	Executable string `yaml:"executable"`
	CodexHome  string `yaml:"codex_home"`
	Endpoint   string `yaml:"endpoint"`
	APIKey     string `yaml:"api_key"`
}

const defaultArtifactTTLSeconds = 86400

type Artifacts struct {
	Root              string `yaml:"root"`
	MaxUploadBytes    int64  `yaml:"max_upload_bytes"`
	QuotaPerUserBytes int64  `yaml:"quota_per_user_bytes"`
	TTLSeconds        int    `yaml:"ttl_seconds"`
}
type Sandbox struct {
	Socket                 string `yaml:"socket"`
	EngineCandidate        string `yaml:"engine_candidate"`
	MaxWorkers             int    `yaml:"max_workers"`
	MemoryCeilingBytes     int64  `yaml:"memory_ceiling_bytes"`
	PIDsCeiling            int    `yaml:"pids_ceiling"`
	ScriptTimeoutCeilingMS int    `yaml:"script_timeout_ceiling_ms"`
	ParallelRPCCeiling     int    `yaml:"parallel_rpc_ceiling"`
	CleanupMS              int    `yaml:"cleanup_ms"`
}
type Execution struct {
	RecoveryMode               bool `yaml:"recovery_mode"`
	ProviderRetryAttemptLimit  int  `yaml:"provider_retry_attempt_limit"`
	ProviderRetryWindowSeconds int  `yaml:"provider_retry_window_seconds"`
	ProviderRetryShortLimit    int  `yaml:"provider_retry_short_limit"`
	ProviderRetryShortBudgetMS int  `yaml:"provider_retry_short_budget_ms"`
	SponsoredAttemptLimit      int  `yaml:"sponsored_attempt_limit"`
	LeaseSeconds               int  `yaml:"lease_seconds"`
	HeartbeatSeconds           int  `yaml:"heartbeat_seconds"`
	ShutdownTargetMS           int  `yaml:"shutdown_target_ms"`
	ShutdownDeadlineMS         int  `yaml:"shutdown_deadline_ms"`
	ActiveRunsPerUserCeiling   int  `yaml:"active_runs_per_user_ceiling"`
	ActiveRunsTotalCeiling     int  `yaml:"active_runs_total_ceiling"`
}
type Model struct {
	InputTokenLimit           int64    `yaml:"input_token_limit"`
	CompactionThresholdTokens int64    `yaml:"compaction_threshold_tokens"`
	CompactionTargetTokens    int64    `yaml:"compaction_target_tokens"`
	ID                        string   `yaml:"id"`
	Provider                  string   `yaml:"provider"`
	Model                     string   `yaml:"model"`
	Efforts                   []string `yaml:"efforts"`
	MaxEffort                 string   `yaml:"max_effort"`
	InputModalities           []string `yaml:"input_modalities"`
	ParallelTools             bool     `yaml:"parallel_tools"`
	Steering                  bool     `yaml:"steering"`
	Questions                 bool     `yaml:"questions"`
	ContextTokens             int      `yaml:"context_tokens"`
	MaxOutputTokens           int      `yaml:"max_output_tokens"`
}
type LongContextTariff struct {
	ThresholdTokens              int64 `yaml:"threshold_tokens"`
	InputCreditsPerMillion       int64 `yaml:"input_credits_per_million"`
	CachedInputCreditsPerMillion int64 `yaml:"cached_input_credits_per_million"`
	CacheWriteCreditsPerMillion  int64 `yaml:"cache_write_credits_per_million"`
	OutputCreditsPerMillion      int64 `yaml:"output_credits_per_million"`
}
type Tariff struct {
	CacheWriteCreditsPerMillion  int64              `yaml:"cache_write_credits_per_million"`
	LongContext                  *LongContextTariff `yaml:"long_context"`
	ModelID                      string             `yaml:"model_id"`
	InputCreditsPerMillion       int64              `yaml:"input_credits_per_million"`
	CachedInputCreditsPerMillion int64              `yaml:"cached_input_credits_per_million"`
	OutputCreditsPerMillion      int64              `yaml:"output_credits_per_million"`
}
type Retention struct {
	Mode string `yaml:"mode"`
	Days int    `yaml:"days"`
}
type Settings struct {
	InitialCredits          int64     `yaml:"initial_credits"`
	MaxRunMicrocredits      int64     `yaml:"max_run_microcredits"`
	DefaultEffort           string    `yaml:"default_effort"`
	HistoryRecentLimit      int       `yaml:"history_recent_limit"`
	MaxSteps                int       `yaml:"max_steps"`
	ModelCatalog            []Model   `yaml:"model_catalog"`
	DefaultModelID          string    `yaml:"default_model_id"`
	Tariffs                 []Tariff  `yaml:"tariffs"`
	UserActiveRunLimit      int       `yaml:"user_active_run_limit"`
	RunActiveTimeSeconds    int       `yaml:"run_active_time_seconds"`
	RunLifetimeSeconds      int       `yaml:"run_lifetime_seconds"`
	InputWaitSeconds        int       `yaml:"input_wait_seconds"`
	ApprovalWaitSeconds     int       `yaml:"approval_wait_seconds"`
	PausedWaitSeconds       int       `yaml:"paused_wait_seconds"`
	ScriptTimeoutMS         int       `yaml:"script_timeout_ms"`
	ParallelRPCLimit        int       `yaml:"parallel_rpc_limit"`
	ContextInputTokenBudget int       `yaml:"context_input_token_budget"`
	OutputTokenLimit        int       `yaml:"output_token_limit"`
	RegexPatternMaxChars    int       `yaml:"regex_pattern_max_chars"`
	RegexScanMaxBytes       int64     `yaml:"regex_scan_max_bytes"`
	RegexDeadlineMS         int       `yaml:"regex_deadline_ms"`
	HistoryRetention        Retention `yaml:"history_retention"`
	// ArtifactRetention is legacy compatibility metadata. Files use Artifacts.TTLSeconds.
	ArtifactRetention         Retention `yaml:"artifact_retention"`
	TraceRetention            Retention `yaml:"trace_retention"`
	ExternalDisclosureDefault bool      `yaml:"external_disclosure_default"`
}
type Observability struct {
	OTel          OTel   `yaml:"otel"`
	LogLevel      string `yaml:"log_level"`
	MetricsListen string `yaml:"metrics_listen"`
	LogPayloads   bool   `yaml:"log_payloads"`
}
type Search struct {
	SemanticEnabled bool `yaml:"semantic_enabled"`
}

type Config struct {
	FileTools         FileTools           `yaml:"file_tools"`
	Transcription     Transcription       `yaml:"transcription"`
	Operator          Operator            `yaml:"operator"`
	Search            Search              `yaml:"search"`
	HTTP              HTTP                `yaml:"http"`
	Database          Database            `yaml:"database"`
	Identity          Identity            `yaml:"identity"`
	Discovery         Discovery           `yaml:"discovery"`
	Providers         map[string]Provider `yaml:"providers"`
	Artifacts         Artifacts           `yaml:"artifacts"`
	Sandbox           Sandbox             `yaml:"sandbox"`
	Execution         Execution           `yaml:"execution"`
	BootstrapSettings Settings            `yaml:"bootstrap_settings"`
	Observability     Observability       `yaml:"observability"`
}

func Load(path string) (Config, error) {
	b, e := os.ReadFile(
		path,
	) // #nosec G304 -- Config path is supplied by the operator at startup, never by an HTTP request.
	if e != nil {
		return Config{}, e
	}
	return Parse(b, os.LookupEnv)
}
func Parse(b []byte, lookup func(string) (string, bool)) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var node yaml.Node
	if e := dec.Decode(&node); e != nil {
		return Config{}, e
	}
	var extra yaml.Node
	if e := dec.Decode(&extra); !errors.Is(e, io.EOF) {
		return Config{}, errors.New("configuration must contain one YAML document")
	}
	var missing []string
	var expand func(*yaml.Node)
	expand = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && n.Tag == "!!str" {
			n.Value = os.Expand(n.Value, func(k string) string {
				v, ok := lookup(k)
				if !ok {
					missing = append(missing, k)
				}
				return v
			})
		}
		for _, child := range n.Content {
			expand(child)
		}
	}
	expand(&node)
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	expanded, e := yaml.Marshal(&node)
	if e != nil {
		return Config{}, e
	}
	strict := yaml.NewDecoder(bytes.NewReader(expanded))
	strict.KnownFields(true)
	c := Config{
		Database:      Database{MigrateOnStart: true},
		Identity:      Identity{PermissionsCacheTTLSeconds: defaultPermissionsCacheTTLSeconds},
		Artifacts:     Artifacts{TTLSeconds: defaultArtifactTTLSeconds},
		Observability: Observability{OTel: OTel{SampleRatio: DefaultOTelSampleRatio}},
		Execution: Execution{
			SponsoredAttemptLimit:      defaultSponsoredAttemptLimit,
			ProviderRetryAttemptLimit:  defaultProviderRetryAttempts,
			ProviderRetryWindowSeconds: defaultProviderRetryWindowSeconds,
			ProviderRetryShortLimit:    maxProviderShortRetries,
			ProviderRetryShortBudgetMS: maxProviderShortBudgetMS,
		},
	}
	if decodeErr := strict.Decode(&c); decodeErr != nil {
		return Config{}, decodeErr
	}
	applyOpenAIDefaults(&c)
	applyFileDefaults(&c)
	c.Observability.OTel.ApplyDefaults()
	c.Identity, e = c.Identity.ResolveCredentials()
	if e != nil {
		return Config{}, e
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if err := c.Observability.OTel.Validate(); err != nil {
		return err
	}
	if err := c.ValidateFileTools(); err != nil {
		return err
	}
	for _, check := range []func() error{c.validateTrust, c.validateDeployment, c.validateLimits, c.validateCatalog, c.validateProviders, c.validateRetention} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateTrust() error {
	if err := c.Identity.ValidateServiceAccounts(); err != nil {
		return err
	}
	if err := c.validateOperator(); err != nil {
		return err
	}
	if c.Sandbox.EngineCandidate != "" && c.Sandbox.EngineCandidate != "goja" {
		return errors.New("only goja sandbox engine is supported")
	}
	switch strings.ToLower(c.Observability.LogLevel) {
	case "", "debug", "info", "warn", "error":
	default:
		return errors.New("log level must be debug, info, warn or error")
	}
	if c.Observability.LogPayloads && !strings.EqualFold(c.Observability.LogLevel, "debug") {
		return errors.New("payload logging requires debug log level")
	}
	if c.HTTP.Listen == "" || c.Database.DSN == "" {
		return errors.New("http.listen and database.dsn required")
	}
	if c.Identity.Issuer == "" || c.Identity.Audience == "" || c.Identity.ClientID == "" ||
		(c.Identity.ClientSecret == "" && c.Identity.ClientPrivateKey == nil) {
		return errors.New("identity trust roots and introspection credentials required")
	}
	if e := validateURL(c.Identity.Issuer); e != nil {
		return e
	}
	if c.Identity.RefreshableExchangeEnabled {
		return errors.New("refreshable exchange requires external Zitadel integration gate; unsupported in MVP")
	}
	return nil
}

func (c Config) validateDeployment() error {
	if err := c.Execution.validateProviderRetry(); err != nil {
		return err
	}
	if err := validateSponsoredAttempts(c.Execution.SponsoredAttemptLimit); err != nil {
		return err
	}
	if c.Execution.ShutdownDeadlineMS <= 0 || c.Execution.ShutdownDeadlineMS > maxShutdownDeadlineMS ||
		c.Execution.ShutdownTargetMS <= 0 ||
		c.Execution.ShutdownTargetMS > c.Execution.ShutdownDeadlineMS {
		return errors.New("shutdown deadline must be positive and at most 5000ms")
	}
	if c.Execution.HeartbeatSeconds <= 0 ||
		c.Execution.LeaseSeconds <= heartbeatLeaseFactor*c.Execution.HeartbeatSeconds {
		return errors.New("lease must exceed twice heartbeat interval")
	}
	if c.HTTP.MaxRequestBytes <= 0 || c.Database.MaxConnections <= 0 || c.Sandbox.MaxWorkers <= 0 ||
		c.Sandbox.ScriptTimeoutCeilingMS <= 0 {
		return errors.New("request size, connections and sandbox limits must be positive")
	}
	return nil
}

func validateSponsoredAttempts(limit int) error {
	if limit < 0 || limit > maxSponsoredAttemptLimit {
		return errors.New("sponsored attempt limit must be between 0 and 8")
	}
	return nil
}

func (c Config) validateLimits() error {
	if c.Identity.PermissionsCacheTTLSeconds < 0 || c.Identity.PermissionsCacheTTLSeconds > 300 {
		return errors.New("permissions cache TTL must be within 0..300 seconds")
	}
	if c.Artifacts.TTLSeconds < 0 || c.Artifacts.TTLSeconds > 604800 {
		return errors.New("artifact TTL must be between 1 and 604800 seconds; zero uses 86400")
	}
	if c.Identity.EncryptionKeyFile == "" || c.Identity.EncryptionKeyID == "" {
		return errors.New("identity encryption key file and ID required")
	}
	for keyID, path := range c.Identity.PreviousEncryptionKeys {
		if keyID == "" || path == "" || keyID == c.Identity.EncryptionKeyID {
			return errors.New("previous encryption key IDs must be nonempty, distinct from active, and have a file")
		}
	}
	if c.Sandbox.MemoryCeilingBytes <= 0 || c.Sandbox.PIDsCeiling <= 0 || c.Sandbox.ParallelRPCCeiling <= 0 ||
		c.Artifacts.MaxUploadBytes <= 0 ||
		c.Artifacts.QuotaPerUserBytes < c.Artifacts.MaxUploadBytes {
		return errors.New("sandbox and artifact limits must be positive and quota cover one upload")
	}
	if c.BootstrapSettings.ScriptTimeoutMS <= 0 ||
		c.BootstrapSettings.ScriptTimeoutMS > c.Sandbox.ScriptTimeoutCeilingMS ||
		c.BootstrapSettings.ParallelRPCLimit <= 0 ||
		c.BootstrapSettings.ParallelRPCLimit > c.Sandbox.ParallelRPCCeiling {
		return errors.New("bootstrap execution limits exceed deployment ceilings")
	}
	if c.Identity.JWKSCacheTTLSeconds <= 0 || c.Identity.JWKSCacheTTLSeconds > maxJWKSCacheTTLSeconds {
		return errors.New("JWKS TTL must be within 1..3600 seconds")
	}
	return nil
}

func (c Config) validateCatalog() error {
	ids := map[string]bool{}
	for _, m := range c.BootstrapSettings.ModelCatalog {
		if m.ID == "" || ids[m.ID] || m.Model == "" || m.ContextTokens <= 0 || m.MaxOutputTokens <= 0 {
			return errors.New("invalid or duplicate model")
		}
		if err := m.validate(c.Providers); err != nil {
			return err
		}
		ids[m.ID] = true
	}
	if !ids[c.BootstrapSettings.DefaultModelID] {
		return errors.New("default model missing")
	}
	if err := c.validateDefaultEffort(); err != nil {
		return err
	}
	tariffIDs := map[string]bool{}
	for _, t := range c.BootstrapSettings.Tariffs {
		if tariffIDs[t.ModelID] {
			return errors.New("duplicate model tariff")
		}
		tariffIDs[t.ModelID] = true
		if err := t.validate(ids); err != nil {
			return err
		}
	}
	for modelID := range ids {
		if !tariffIDs[modelID] {
			return errors.New("every model needs an explicit tariff")
		}
	}
	if c.BootstrapSettings.InitialCredits < 0 || c.BootstrapSettings.MaxRunMicrocredits < 0 {
		return errors.New("credit limits must be nonnegative")
	}
	return nil
}

func (c Config) validateDefaultEffort() error {
	for _, model := range c.BootstrapSettings.ModelCatalog {
		if model.ID != c.BootstrapSettings.DefaultModelID {
			continue
		}
		effort := c.BootstrapSettings.DefaultEffort
		index := slices.Index(model.Efforts, effort)
		ceiling := slices.Index(model.Efforts, model.MaxEffort)
		if len(model.Efforts) == 0 && effort == "" {
			return nil
		}
		if index < 0 || (model.MaxEffort != "" && (ceiling < 0 || index > ceiling)) {
			return errors.New("default effort is not allowed by the default model")
		}
	}
	return nil
}

func (c Config) validateProviders() error {
	for _, p := range c.Providers {
		if err := p.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p Provider) validate() error {
	switch p.Kind {
	case "", "responses":
		if p.Mailbox != "" || p.Executable != "" || p.CodexHome != "" {
			return errors.New("responses provider cannot use development provider settings")
		}
	case "mock":
		if !filepath.IsAbs(p.Mailbox) || p.Executable != "" || p.CodexHome != "" || p.Endpoint != "" ||
			p.APIKey != "" {
			return errors.New("mock provider requires an absolute mailbox only")
		}
		return nil
	case "codex":
		if !filepath.IsAbs(p.Executable) || !filepath.IsAbs(p.CodexHome) || p.Mailbox != "" || p.Endpoint != "" ||
			p.APIKey != "" {
			return errors.New("codex provider requires absolute executable and dedicated home only")
		}
		return nil
	default:
		return errors.New("unknown provider kind")
	}
	if e := validateURL(p.Endpoint); e != nil {
		return e
	}
	if p.APIKey == "" {
		return errors.New("provider API key required")
	}
	return nil
}

func (c Config) validateRetention() error {
	if c.BootstrapSettings.TraceRetention.Mode != "" && c.BootstrapSettings.TraceRetention.Mode != "forever" {
		return errors.New(
			"trace duration retention is not supported; use forever until a diagnostic trace store exists",
		)
	}
	for _, r := range []Retention{c.BootstrapSettings.HistoryRetention, c.BootstrapSettings.ArtifactRetention} {
		if r.Mode != "forever" && (r.Mode != "duration" || r.Days <= 0) {
			return errors.New("retention must be forever or duration with positive days")
		}
	}
	return nil
}
func validateURL(s string) error {
	u, e := url.Parse(s)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" ||
		(u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("expected HTTP(S) URL without credentials, query or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return errors.New("HTTPS required outside loopback")
	}
	return nil
}
func LoadEncryptionKey(path string) ([]byte, error) {
	b, e := os.ReadFile(
		path,
	) // #nosec G304 -- Credential file path comes from validated operator configuration, never from a client or manifest.
	if e != nil {
		return nil, e
	}
	if len(b) == encryptionKeyBytes {
		return bytes.Clone(b), nil
	}
	decoded, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if e != nil || len(decoded) != encryptionKeyBytes {
		return nil, errors.New("encryption key must contain 32 raw bytes or base64 thereof")
	}
	return decoded, nil
}

// ValidateIdentityURL validates an immutable OIDC trust root.
func ValidateIdentityURL(issuer string) error { return validateURL(issuer) }

func (m Model) validate(providers map[string]Provider) error {
	if m.InputTokenLimit < 0 ||
		(m.InputTokenLimit > 0 && (m.InputTokenLimit > int64(m.ContextTokens) || m.CompactionThresholdTokens > m.InputTokenLimit)) {
		return errors.New("invalid model input token limit")
	}
	if m.CompactionThresholdTokens < 0 || m.CompactionTargetTokens < 0 ||
		(m.CompactionThresholdTokens > 0 && (m.CompactionThresholdTokens > int64(m.ContextTokens) || m.CompactionTargetTokens >= m.CompactionThresholdTokens)) ||
		(m.CompactionThresholdTokens == 0 && m.CompactionTargetTokens != 0) {
		return errors.New("invalid model compaction limits")
	}
	if _, ok := providers[m.Provider]; !ok {
		return errors.New("model provider missing")
	}
	return nil
}

func (t Tariff) validate(ids map[string]bool) error {
	if !ids[t.ModelID] || t.InputCreditsPerMillion < 0 || t.CachedInputCreditsPerMillion < 0 ||
		t.OutputCreditsPerMillion < 0 ||
		t.CacheWriteCreditsPerMillion < 0 {
		return errors.New("invalid tariff")
	}
	if band := t.LongContext; band != nil &&
		(band.ThresholdTokens <= 0 || band.InputCreditsPerMillion < 0 || band.CachedInputCreditsPerMillion < 0 || band.CacheWriteCreditsPerMillion < 0 || band.OutputCreditsPerMillion < 0) {
		return errors.New("invalid long context tariff")
	}
	return nil
}
