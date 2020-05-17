package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Microsoft/ApplicationInsights-Go/appinsights"
	"github.com/microsoft/ApplicationInsights-Go/appinsights/contracts"
	"github.com/fluent/fluent-bit-go/output"
)

var (
	// FlushedRecordsCount indicates the number of flushed log records in the current period
	FlushedRecordsCount float64
	// FlushedRecordsSize indicates the size of the flushed records in the current period
	FlushedRecordsSize float64
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
	//Tracks the number of telegraf metrics sent successfully between telemetry ticker periods (uses ContainerLogTelemetryTicker)
	TelegrafMetricsSentCount float64
	//Tracks the number of send errors between telemetry ticker periods (uses ContainerLogTelemetryTicker)
	TelegrafMetricsSendErrorCount float64
	//Tracks the number of 429 (throttle) errors between telemetry ticker periods (uses ContainerLogTelemetryTicker)
	TelegrafMetricsSend429ErrorCount float64
)

const (
	clusterTypeACS                                    = "ACS"
	clusterTypeAKS                                    = "AKS"
	envAKSResourceID                                  = "AKS_RESOURCE_ID"
	envACSResourceName                                = "ACS_RESOURCE_NAME"
	envAppInsightsAuth                                = "APPLICATIONINSIGHTS_AUTH"
	envAppInsightsEndpoint                            = "APPLICATIONINSIGHTS_ENDPOINT"
	metricNameAvgFlushRate                            = "ContainerLogAvgRecordsFlushedPerSec"
	metricNameAvgLogGenerationRate                    = "ContainerLogsGeneratedPerSec"
	metricNameLogSize                                 = "ContainerLogsSize"
	metricNameAgentLogProcessingMaxLatencyMs          = "ContainerLogsAgentSideLatencyMs"
	metricNameNumberofTelegrafMetricsSentSuccessfully = "TelegrafMetricsSentCount"
	metricNameNumberofSendErrorsTelegrafMetrics       = "TelegrafMetricsSendErrorCount"
	metricNameNumberofSend429ErrorsTelegrafMetrics    = "TelegrafMetricsSend429ErrorCount"

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
		elapsed := time.Since(start)

		ContainerLogTelemetryMutex.Lock()
		flushRate := FlushedRecordsCount / FlushedRecordsTimeTaken * 1000
		logRate := FlushedRecordsCount / float64(elapsed/time.Second)
		logSizeRate := FlushedRecordsSize / float64(elapsed/time.Second)
		telegrafMetricsSentCount := TelegrafMetricsSentCount
		telegrafMetricsSendErrorCount := TelegrafMetricsSendErrorCount
		telegrafMetricsSend429ErrorCount := TelegrafMetricsSend429ErrorCount
		TelegrafMetricsSentCount = 0.0
		TelegrafMetricsSendErrorCount = 0.0
		TelegrafMetricsSend429ErrorCount = 0.0
		FlushedRecordsCount = 0.0
		FlushedRecordsSize = 0.0
		FlushedRecordsTimeTaken = 0.0
		logLatencyMs := AgentLogProcessingMaxLatencyMs
		logLatencyMsContainer := AgentLogProcessingMaxLatencyMsContainer
		AgentLogProcessingMaxLatencyMs = 0
		AgentLogProcessingMaxLatencyMsContainer = ""
		ContainerLogTelemetryMutex.Unlock()

		if strings.Compare(strings.ToLower(os.Getenv("CONTROLLER_TYPE")), "daemonset") == 0 {
			SendEvent(eventNameDaemonSetHeartbeat, make(map[string]string))
			flushRateMetric := appinsights.NewMetricTelemetry(metricNameAvgFlushRate, flushRate)
			TelemetryClient.Track(flushRateMetric)
			logRateMetric := appinsights.NewMetricTelemetry(metricNameAvgLogGenerationRate, logRate)
			logSizeMetric := appinsights.NewMetricTelemetry(metricNameLogSize, logSizeRate)
			TelemetryClient.Track(logRateMetric)
			Log("Log Size Rate: %f\n", logSizeRate)
			TelemetryClient.Track(logSizeMetric)
			logLatencyMetric := appinsights.NewMetricTelemetry(metricNameAgentLogProcessingMaxLatencyMs, logLatencyMs)
			logLatencyMetric.Properties["Container"] = logLatencyMsContainer
			TelemetryClient.Track(logLatencyMetric)
		}
		TelemetryClient.Track(appinsights.NewMetricTelemetry(metricNameNumberofTelegrafMetricsSentSuccessfully, telegrafMetricsSentCount))
		if telegrafMetricsSendErrorCount > 0.0 {
			TelemetryClient.Track(appinsights.NewMetricTelemetry(metricNameNumberofSendErrorsTelegrafMetrics, telegrafMetricsSendErrorCount))
		}
		if telegrafMetricsSend429ErrorCount > 0.0 {
			TelemetryClient.Track(appinsights.NewMetricTelemetry(metricNameNumberofSend429ErrorsTelegrafMetrics, telegrafMetricsSend429ErrorCount))
		}
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

	appInsightsEndpoint := os.Getenv(envAppInsightsEndpoint)
	telemetryClientConfig := appinsights.NewTelemetryConfiguration(string(decIkey))
	// endpoint override required only for sovereign clouds
	if appInsightsEndpoint != "" {
		Log("Overriding the default AppInsights EndpointUrl with %s", appInsightsEndpoint)
		telemetryClientConfig.EndpointUrl = envAppInsightsEndpoint
	}
	// if the proxy configured set the customized httpclient with proxy
	if ProxyEndpoint != "" {		
		proxyEndpointUrl, err := url.Parse(ProxyEndpoint)		
		if err != nil {
			Log("Failed Parsing of Proxy endpoint %s", err.Error())			
			return -1, err
		}		
		//adding the proxy settings to the Transport object
		transport := &http.Transport{
			Proxy: http.ProxyURL(proxyEndpointUrl),
		}
		httpClient := &http.Client{
			Transport: transport,
		}
		telemetryClientConfig.Client = httpClient
	}
	TelemetryClient = appinsights.NewTelemetryClientFromConfig(telemetryClientConfig)

	telemetryOffSwitch := os.Getenv("DISABLE_TELEMETRY")
	if strings.Compare(strings.ToLower(telemetryOffSwitch), "true") == 0 {
		Log("Appinsights telemetry is disabled \n")
		TelemetryClient.SetIsEnabled(false)
	}

	CommonProperties = make(map[string]string)
	CommonProperties["Computer"] = Computer
	CommonProperties["WorkspaceID"] = WorkspaceID
	CommonProperties["ControllerType"] = os.Getenv("CONTROLLER_TYPE")
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
func PushToAppInsightsTraces(records []map[interface{}]interface{}, severityLevel contracts.SeverityLevel, tag string) int {
	var logLines []string
	for _, record := range records {
		// If record contains config error or prometheus scraping errors send it to KubeMonAgentEvents table
		var logEntry = ToString(record["log"])
		if strings.Contains(logEntry, "config::error") {
			populateKubeMonAgentEventHash(record, ConfigError)
		} else if strings.Contains(logEntry, "E! [inputs.prometheus]") {
			populateKubeMonAgentEventHash(record, PromScrapingError)
		} else {
			logLines = append(logLines, logEntry)
		}
	}

	traceEntry := strings.Join(logLines, "\n")
	traceTelemetryItem := appinsights.NewTraceTelemetry(traceEntry, severityLevel)
	traceTelemetryItem.Properties["tag"] = tag
	TelemetryClient.Track(traceTelemetryItem)
	return output.FLB_OK
}
