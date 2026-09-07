package driver

import (
	"context"
	"fmt"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func kernelspecPayloadReady(payload []byte) bool {
	body := string(payload)
	return strings.Contains(body, "javascript") || strings.Contains(body, "python3") || strings.Contains(body, "bash")
}

func jupyterBaseURL(proxyState ProxyState) string {
	baseURL := strings.TrimSpace(proxyState.ProxyPath)
	if baseURL == "" {
		return "/"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/lab")
	baseURL = strings.TrimRight(baseURL, "/") + "/"
	if !strings.HasPrefix(baseURL, "/") {
		baseURL = "/" + baseURL
	}
	return baseURL
}

func jupyterDirectURL(proxyState ProxyState) string {
	if !jupyterEnabled(proxyState) {
		return ""
	}
	baseURL := strings.TrimRight(jupyterBaseURL(proxyState), "/")
	return fmt.Sprintf("http://127.0.0.1:%d%s/lab?token=%s", proxyState.HostPort, baseURL, url.QueryEscape(proxyState.Token))
}

func jupyterConnectTarget(proxyState ProxyState) (host string, port int) {
	if !jupyterEnabled(proxyState) {
		return "127.0.0.1", 0
	}
	guestHost := strings.TrimSpace(proxyState.GuestHost)
	if guestHost != "" && guestHost != "127.0.0.1" && proxyState.GuestPort > 0 {
		return guestHost, proxyState.GuestPort
	}
	return "127.0.0.1", proxyState.HostPort
}

func JupyterConnectTarget(proxyState ProxyState) (host string, port int) {
	return jupyterConnectTarget(proxyState)
}

func jupyterConnectAddress(proxyState ProxyState) string {
	host, port := jupyterConnectTarget(proxyState)
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func JupyterConnectAddress(proxyState ProxyState) string {
	return jupyterConnectAddress(proxyState)
}

func jupyterEnabled(proxyState ProxyState) bool {
	return proxyState.Enabled && proxyState.GuestPort > 0
}

func jupyterKernelspecsURL(proxyState ProxyState) string {
	return fmt.Sprintf("http://%s%sapi/kernelspecs?token=%s", jupyterConnectAddress(proxyState), jupyterBaseURL(proxyState), url.QueryEscape(proxyState.Token))
}

func waitForJupyterProxy(ctx context.Context, proxyState ProxyState) error {
	urlValue := jupyterKernelspecsURL(proxyState)
	client := newJupyterReadyHTTPClient(5 * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlValue, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			func() {
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode >= 200 && resp.StatusCode < 500 {
					payload, readErr := io.ReadAll(resp.Body)
					if readErr != nil {
						lastErr = readErr
						return
					}
					if kernelspecPayloadReady(payload) {
						lastErr = nil
						return
					}
					lastErr = fmt.Errorf("unexpected jupyter payload on %s", urlValue)
					return
				}
				lastErr = fmt.Errorf("unexpected status %d from %s", resp.StatusCode, urlValue)
			}()
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("jupyter did not become ready on %s: %w", urlValue, lastErr)
			}
			return fmt.Errorf("jupyter did not become ready on %s: %w", urlValue, ctx.Err())
		case <-ticker.C:
		}
	}
}

func newJupyterReadyHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func WaitForJupyterProxy(ctx context.Context, proxyState ProxyState) error {
	return waitForJupyterProxy(ctx, proxyState)
}

func jupyterLogDir(config *appconfig.Config) string {
	return config.GuestLogRoot
}

func jupyterLogPath(config *appconfig.Config) string {
	return filepath.Join(jupyterLogDir(config), "jupyter.log")
}

func readSandboxJupyterLog(session *Sandbox) string {
	if session == nil {
		return ""
	}
	logPath := filepath.Join(filepath.Dir(session.Summary.WorkspacePath), "logs", "jupyter.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func jupyterLogIndicatesReady(logText string) bool {
	logText = strings.TrimSpace(logText)
	if logText == "" {
		return false
	}
	return strings.Contains(logText, "Jupyter Server") && strings.Contains(logText, "is running at:")
}

func jupyterLaunchCommand(config *appconfig.Config, proxyState ProxyState, background bool) string {
	return jupyterLaunchCommandWithBootstrap(config, proxyState, background, false)
}

func directoryOnlyJupyterLaunchCommand(config *appconfig.Config, proxyState ProxyState, background bool) string {
	return jupyterLaunchCommandWithBootstrap(config, proxyState, background, true)
}

func jupyterLaunchCommandWithBootstrap(config *appconfig.Config, proxyState ProxyState, background bool, includeDirectoryOnlyBootstrap bool) string {
	appconfig.ApplyDefaultGuestPaths(config)
	logDir := jupyterLogDir(config)
	logPath := jupyterLogPath(config)
	launch := "python3 -m jupyterlab --ServerApp.ip=0.0.0.0 --ServerApp.port=" + fmt.Sprintf("%d", proxyState.GuestPort) +
		" --ServerApp.root_dir=\"" + config.GuestWorkspacePath + "\"" +
		" --ServerApp.base_url=\"" + strings.TrimRight(jupyterBaseURL(proxyState), "/") + "\"" +
		" --IdentityProvider.token=\"" + proxyState.Token + "\"" +
		" --ServerApp.password= --ServerApp.allow_origin='' --ServerApp.disable_check_xsrf=False" +
		" --allow-root --no-browser"
	if background {
		// Keep the trailing ampersand scoped to the Jupyter launch. Without
		// grouping, the shell backgrounds the entire && chain and microsandbox
		// tears Jupyter down when it closes the still-active exec session.
		launch = "(nohup " + launch + " > \"" + logPath + "\" 2>&1 < /dev/null &)"
	} else {
		launch = "exec " + launch + " > \"" + logPath + "\" 2>&1"
	}
	commands := []string{
		"set -eux",
		"mkdir -p \"" + config.GuestWorkspacePath + "\" \"" + config.GuestHomePath + "\" \"" + logDir + "\"",
		runtimeSmokeMarkerCommand(),
		"echo \"[agent-compose] starting jupyter\" > \"" + logPath + "\"",
		"echo \"[agent-compose] pwd=$(pwd)\" >> \"" + logPath + "\"",
		"echo \"[agent-compose] whoami=$(whoami 2>/dev/null || true)\" >> \"" + logPath + "\"",
		"echo \"[agent-compose] python3=$(command -v python3 2>/dev/null || echo missing)\" >> \"" + logPath + "\"",
		"echo \"[agent-compose] node=$(command -v node 2>/dev/null || echo missing)\" >> \"" + logPath + "\"",
		"echo \"[agent-compose] workspace=" + config.GuestWorkspacePath + "\" >> \"" + logPath + "\"",
	}
	if !background {
		commands = append(commands, "python3 -c \"import jupyterlab; print('[agent-compose] jupyterlab=' + getattr(jupyterlab, '__version__', 'unknown'))\" >> \""+logPath+"\" 2>&1")
	}
	commands = append(commands, launch)
	if includeDirectoryOnlyBootstrap {
		commands = append(commands[:1], append([]string{directoryOnlyGuestSandboxBootstrapCommand(config)}, commands[1:]...)...)
	}
	return strings.Join(commands, " && ")
}

func runtimeSmokeMarkerCommand() string {
	return "if [ -n \"${SMOKE_MARKER:-}\" ]; then " +
		"test -f /root/.claude.json; " +
		"test -f /root/.gitconfig; " +
		"printf ok > /root/.codex/runtime-mount-smoke-home.txt; " +
		"printf ok > \"${SMOKE_MARKER}\"; " +
		"fi"
}
