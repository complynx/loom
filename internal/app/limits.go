package app

import "time"

const (
	maintenanceTimeout      = 3 * time.Second
	maintenanceBatch        = 100
	telemetryInterval       = 15 * time.Second
	telemetryTimeout        = 2 * time.Second
	inferenceTimeout        = 2 * time.Minute
	outcomeUnknown          = "unknown"
	operationUnknown        = "operation_unknown"
	historyModule           = "history"
	artifactsModule         = "artifacts"
	parallelRPCLimit        = 8
	checkpointTimeout       = 500 * time.Millisecond
	queueMaxAttempts        = 5
	databaseConnectTimeout  = 3 * time.Second
	discoveryRefreshTimeout = 3 * time.Second
	apiReadHeaderTimeout    = 5 * time.Second
)

const (
	apiIdleTimeout        = 30 * time.Second
	apiMaxHeaderBytes     = 16 << 10
	metricsRequestTimeout = 3 * time.Second
	metricsIdleTimeout    = 15 * time.Second
	metricsMaxHeaderBytes = 8192
	modelMaxInputBytes    = 8 << 20
	dockerWriteTimeout    = 4 * time.Second
	dockerIdleTimeout     = 15 * time.Second
	dockerMaxHeaderBytes  = 4096
)

const toolFunctionType = "function"
const builtinReadMethod = "read"
