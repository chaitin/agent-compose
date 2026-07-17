package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"fmt"
	"io"
	"text/tabwriter"
)

type composeStatsOutput struct {
	SandboxID        string              `json:"sandbox_id"`
	SandboxShortID   string              `json:"sandbox_short_id"`
	Driver           string              `json:"driver"`
	SampledAt        string              `json:"sampled_at"`
	CPUPercent       composeMetricOutput `json:"cpu_percent"`
	MemoryUsageBytes composeMetricOutput `json:"memory_usage_bytes"`
	MemoryLimitBytes composeMetricOutput `json:"memory_limit_bytes"`
	MemoryPercent    composeMetricOutput `json:"memory_percent"`
	NetworkRxBytes   composeMetricOutput `json:"network_rx_bytes"`
	NetworkTxBytes   composeMetricOutput `json:"network_tx_bytes"`
	BlockReadBytes   composeMetricOutput `json:"block_read_bytes"`
	BlockWriteBytes  composeMetricOutput `json:"block_write_bytes"`
	UptimeSeconds    composeMetricOutput `json:"uptime_seconds"`
}

type composeProjectStatsOutput struct {
	Project composeUpProjectOutput `json:"project"`
	Stats   []composeStatsOutput   `json:"stats"`
}

type composeMetricOutput struct {
	Value   *float64 `json:"value"`
	Unit    string   `json:"unit"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
}

func composeStatsOutputFromProto(stats *agentcomposev2.SandboxStats) composeStatsOutput {
	if stats == nil {
		return composeStatsOutput{}
	}
	return composeStatsOutput{
		SandboxID:        displayOpaqueID(stats.GetSandboxId()),
		SandboxShortID:   shortOpaqueID(stats.GetSandboxId()),
		Driver:           stats.GetDriver(),
		SampledAt:        stats.GetSampledAt(),
		CPUPercent:       composeMetricOutputFromProto(stats.GetCpuPercent()),
		MemoryUsageBytes: composeMetricOutputFromProto(stats.GetMemoryUsageBytes()),
		MemoryLimitBytes: composeMetricOutputFromProto(stats.GetMemoryLimitBytes()),
		MemoryPercent:    composeMetricOutputFromProto(stats.GetMemoryPercent()),
		NetworkRxBytes:   composeMetricOutputFromProto(stats.GetNetworkRxBytes()),
		NetworkTxBytes:   composeMetricOutputFromProto(stats.GetNetworkTxBytes()),
		BlockReadBytes:   composeMetricOutputFromProto(stats.GetBlockReadBytes()),
		BlockWriteBytes:  composeMetricOutputFromProto(stats.GetBlockWriteBytes()),
		UptimeSeconds:    composeMetricOutputFromProto(stats.GetUptimeSeconds()),
	}
}

func composeProjectStatsOutputFromProject(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project) (composeProjectStatsOutput, error) {
	output := composeProjectStatsOutput{
		Project: composeProjectSummaryOutput(project.GetSummary()),
	}
	psOutput, err := composePSOutputFromProject(ctx, clients, project, composePSOptions{})
	if err != nil {
		return composeProjectStatsOutput{}, err
	}
	output.Stats = make([]composeStatsOutput, 0, len(psOutput.Sandboxes))
	for _, sandbox := range psOutput.Sandboxes {
		sandboxID := firstNonEmptyString(sandbox.RawID, sandbox.SandboxID)
		stats, err := composeStatsOutputForSandbox(ctx, clients.sandbox, sandboxID)
		if err != nil {
			return composeProjectStatsOutput{}, fmt.Errorf("get sandbox %s stats: %w", sandbox.SandboxID, err)
		}
		output.Stats = append(output.Stats, stats)
	}
	return output, nil
}

func composeMetricOutputFromProto(metric *agentcomposev2.MetricValue) composeMetricOutput {
	if metric == nil {
		return composeMetricOutput{Status: "unknown"}
	}
	return composeMetricOutput{
		Value:   metric.Value,
		Unit:    metric.GetUnit(),
		Status:  metricStatusText(metric.GetStatus()),
		Message: metric.GetMessage(),
	}
}

func metricStatusText(status agentcomposev2.MetricStatus) string {
	switch status {
	case agentcomposev2.MetricStatus_METRIC_STATUS_OK:
		return "ok"
	case agentcomposev2.MetricStatus_METRIC_STATUS_UNAVAILABLE:
		return "unavailable"
	case agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN, agentcomposev2.MetricStatus_METRIC_STATUS_UNSPECIFIED:
		fallthrough
	default:
		return "unknown"
	}
}

func writeStatsText(out io.Writer, stats []composeStatsOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SANDBOX\tDRIVER\tCPU%\tMEM\tMEM_LIMIT\tMEM%\tNET_RX\tNET_TX\tBLOCK_READ\tBLOCK_WRITE\tUPTIME"); err != nil {
		return err
	}
	for _, output := range stats {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(output.SandboxShortID, shortOpaqueID(output.SandboxID), "-"),
			firstNonEmptyString(output.Driver, "-"),
			formatMetricForText(output.CPUPercent),
			formatMetricForText(output.MemoryUsageBytes),
			formatMetricForText(output.MemoryLimitBytes),
			formatMetricForText(output.MemoryPercent),
			formatMetricForText(output.NetworkRxBytes),
			formatMetricForText(output.NetworkTxBytes),
			formatMetricForText(output.BlockReadBytes),
			formatMetricForText(output.BlockWriteBytes),
			formatMetricForText(output.UptimeSeconds),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func formatMetricForText(metric composeMetricOutput) string {
	if metric.Status != "ok" || metric.Value == nil {
		return "-"
	}
	switch metric.Unit {
	case "percent":
		return fmt.Sprintf("%.2f", *metric.Value)
	case "seconds":
		return fmt.Sprintf("%.0fs", *metric.Value)
	default:
		return fmt.Sprintf("%.0f", *metric.Value)
	}
}
