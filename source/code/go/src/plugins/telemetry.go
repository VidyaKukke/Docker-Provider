package main

import (
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Microsoft/ApplicationInsights-Go/appinsights"
	"github.com/fluent/fluent-bit-go/output"
)

var (
	// FlushedRecordsCount indicates the number of flushed records in the current period
	FlushedRecordsCount float64
	// FlushedRecordsTimeTaken indicates the cumulative time taken to flush the records for the current period
	FlushedRecordsTimeTaken float64
	// This is telemetry for how old/latent logs we are processing in milliseconds (max over a period of time)
	AgentLogProcessingMaxLatencyMs float64
	// This is telemetry for which container logs were latent (max over a period of time)
	AgentLogProcessingMaxLatencyMsContainer string
	// CommonProperties indicates the dimensions that are sent with every event/metric
	CommonProperties map[string]string
	// TelemetryClient is the client used to send the telemetry
	TelemetryClient appinsights.TelemetryClient
	// ContainerLogTelemetryTicker sends telemetry periodically
	ContainerLogTelemetryTicker *time.Ticker
)

const (
	clusterTypeACS                      = "ACS"
	clusterTypeAKS                      = "AKS"
	controllerTypeDaemonSet             = "DaemonSet"
	controllerTypeReplicaSet            = "ReplicaSet"
	envAKSResourceID                    = "AKS_RESOURCE_ID"
	envACSResourceName                  = "ACS_RESOURCE_NAME"
	envAppInsightsAuth                  = "APPLICATIONINSIGHTS_AUTH"
	metricNameAvgFlushRate              = "ContainerLogAvgRecordsFlushedPerSec"
	metricNameAvgLogGenerationRate      = "ContainerLogsGeneratedPerSec"
	metricNameAgentLogProcessingMaxLatencyMs = "ContainerLogsAgentSideLatencyMs"
	defaultTelemetryPushIntervalSeconds = 300

	eventNameContainerLogInit   = "ContainerLogPluginInitialized"
	eventNameDaemonSetHeartbeat = "ContainerLogDaemonSetHeartbeatEvent"
)

// SendContainerLogPluginMetrics is a go-routine that flushes the data periodically (every 5 mins to App Insights)
func SendContainerLogPluginMetrics(telemetryPushIntervalProperty string) {
	telemetryPushInterval, err := strconv.Atoi(telemetryPushIntervalProperty)
	if err != nil {
		Log("Error Converting telemetryPushIntervalProperty %s. Using Default Interval... %d \n", telemetryPushIntervalProperty, defaultTelemetryPushIntervalSeconds)
		telemetryPushInterval = defaultTelemetryPushIntervalSeconds
	}

	ContainerLogTelemetryTicker = time.NewTicker(time.Second * time.Duration(telemetryPushInterval))

	start := time.Now()
	SendEvent(eventNameContainerLogInit, make(map[string]string))

	for ; true; <-ContainerLogTelemetryTicker.C {
		SendEvent(eventNameDaemonSetHeartbeat, make(map[string]string))
		elapsed := time.Since(start)
		ContainerLogTelemetryMutex.Lock()
		flushRate := FlushedRecordsCount / FlushedRecordsTimeTaken * 1000
		logRate := FlushedRecordsCount / float64(elapsed/time.Second)
		FlushedRecordsCount = 0.0
		FlushedRecordsTimeTaken = 0.0
		logLatencyMs := AgentLogProcessingMaxLatencyMs
		logLatencyMsContainer := AgentLogProcessingMaxLatencyMsContainer
		AgentLogProcessingMaxLatencyMs = 0
		AgentLogProcessingMaxLatencyMsContainer = ""
		ContainerLogTelemetryMutex.Unlock()

		flushRateMetric := appinsights.NewMetricTelemetry(metricNameAvgFlushRate, flushRate)
		TelemetryClient.Track(flushRateMetric)
		logRateMetric := appinsights.NewMetricTelemetry(metricNameAvgLogGenerationRate, logRate)
		TelemetryClient.Track(logRateMetric)
		logLatencyMetric := appinsights.NewMetricTelemetry(metricNameAgentLogProcessingMaxLatencyMs, logLatencyMs)
		logLatencyMetric.Properties["Container"] = logLatencyMsContainer
		TelemetryClient.Track(logLatencyMetric)
		start = time.Now()
	}
}

// SendEvent sends an event to App Insights
func SendEvent(eventName string, dimensions map[string]string) {
	Log("Sending Event : %s\n", eventName)
	event := appinsights.NewEventTelemetry(eventName)

	// add any extra Properties
	for k, v := range dimensions {
		event.Properties[k] = v
	}

	TelemetryClient.Track(event)
}

// SendException  send an event to the configured app insights instance
func SendException(err interface{}) {
	if TelemetryClient != nil {
		TelemetryClient.TrackException(err)
	}
}

// InitializeTelemetryClient sets up the telemetry client to send telemetry to the App Insights instance
func InitializeTelemetryClient(agentVersion string) (int, error) {
	encodedIkey := os.Getenv(envAppInsightsAuth)
	if encodedIkey == "" {
		Log("Environment Variable Missing \n")
		return -1, errors.New("Missing Environment Variable")
	}

	decIkey, err := base64.StdEncoding.DecodeString(encodedIkey)
	if err != nil {
		Log("Decoding Error %s", err.Error())
		return -1, err
	}

	TelemetryClient = appinsights.NewTelemetryClient(string(decIkey))

	CommonProperties = make(map[string]string)
	CommonProperties["Computer"] = Computer
	CommonProperties["WorkspaceID"] = WorkspaceID
	CommonProperties["ControllerType"] = controllerTypeDaemonSet
	CommonProperties["AgentVersion"] = agentVersion

	aksResourceID := os.Getenv(envAKSResourceID)
	// if the aks resource id is not defined, it is most likely an ACS Cluster
	if aksResourceID == "" {
		CommonProperties["ACSResourceName"] = os.Getenv(envACSResourceName)
		CommonProperties["ClusterType"] = clusterTypeACS

		CommonProperties["SubscriptionID"] = ""
		CommonProperties["ResourceGroupName"] = ""
		CommonProperties["ClusterName"] = ""
		CommonProperties["Region"] = ""
		CommonProperties["AKS_RESOURCE_ID"] = ""

	} else {
		CommonProperties["ACSResourceName"] = ""
		CommonProperties["AKS_RESOURCE_ID"] = aksResourceID
		splitStrings := strings.Split(aksResourceID, "/")
		if len(splitStrings) > 0 && len(splitStrings) < 10 {
			CommonProperties["SubscriptionID"] = splitStrings[2]
			CommonProperties["ResourceGroupName"] = splitStrings[4]
			CommonProperties["ClusterName"] = splitStrings[8]
		}
		CommonProperties["ClusterType"] = clusterTypeAKS

		region := os.Getenv("AKS_REGION")
		CommonProperties["Region"] = region
	}

	TelemetryClient.Context().CommonProperties = CommonProperties
	return 0, nil
}

// PushToAppInsightsTraces sends the log lines as trace messages to the configured App Insights Instance
func PushToAppInsightsTraces(records []map[interface{}]interface{}) int {
	var logLines []string
	for _, record := range records {
		logLines = append(logLines, ToString(record["log"]))
	}

	traceEntry := strings.Join(logLines, "\n")
	TelemetryClient.TrackTrace(traceEntry, 1)
	return output.FLB_OK
}