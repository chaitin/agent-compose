package sessions

import (
	"strings"
	"time"

	domain "agent-compose/pkg/model"
)

func ApplySessionStartInfo(vmState domain.VMState, proxyState domain.ProxyState, info domain.SandboxVMInfo, now time.Time) (domain.VMState, domain.ProxyState) {
	vmState.BoxID = firstNonEmpty(info.BoxID, vmState.BoxID)
	vmState.StartedAt = now.UTC()
	vmState.StoppedAt = time.Time{}
	vmState.LastError = ""
	vmState.BootstrapRef = firstNonEmpty(info.JupyterURL, vmState.BootstrapRef)
	if info.NetworkState != nil {
		vmState.NetworkState = info.NetworkState
	}
	if info.ProxyState != nil {
		proxyState = *info.ProxyState
	}
	proxyState.JupyterURL = firstNonEmpty(info.JupyterURL, proxyState.JupyterURL)
	return vmState, proxyState
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
