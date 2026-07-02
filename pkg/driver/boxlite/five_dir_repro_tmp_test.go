//go:build boxlitecgo && boxlite_repro

package boxlitedriver

// 这个文件里的测试是手动 repro 用例，日常 go test 和 CI 都不会执行。
//
// 运行方式：
//   task test:boxlite-mount-repro
//
// 用途：
//   复现旧版 BoxLite manifest 一次性挂载 5 个 host 目录时，
//   libkrun 启动阶段稳定出现 RegisterBlockDevice(IrqsExhausted) 的问题。
//   测试成功代表问题被复现，因此会保留 /tmp/agent-compose-smoke-* 现场目录供排查。
//
// 隔离方式：
//   文件使用 boxlitecgo && boxlite_repro build tag。只有显式传入
//   -tags 'boxlitecgo,boxlite_repro' 时才会被编译和执行。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManualBoxLiteFiveDirMountRepro(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	for i := 0; i < 3; i++ {
		t.Run("run", func(t *testing.T) {
			config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
			session, vmState, proxyState := newRuntimeSmokeSession(t, ctx, config, RuntimeDriverBoxlite)
			writeFiveDirBoxLiteManifest(t, session)

			runtime := &cgoBoxRuntime{config: config}
			box, created, err := runtime.getOrCreateBox(ctx, session, vmState, proxyState)
			if err != nil {
				t.Fatalf("getOrCreateBox returned error: %v", err)
			}
			defer box.free()
			boxID, err := runtime.boxID(box)
			if err != nil {
				t.Fatalf("boxID returned error: %v", err)
			}
			t.Logf("box_id=%s boxlite_home=%s", boxID, config.BoxliteHome)
			if !created {
				t.Fatalf("expected fresh box")
			}
			err = runtime.startBox(ctx, box)
			if err == nil {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), config.SessionStopTimeout)
				defer stopCancel()
				_, _ = runtime.StopSession(stopCtx, session, VMState{BoxID: boxID})
				t.Fatalf("startBox unexpectedly succeeded")
			}
			logText := readBoxLiteReproLogs(t, config.BoxliteHome, boxID)
			t.Logf("startBox error: %v", err)
			t.Logf("boxlite logs:\n%s", logText)
			if !strings.Contains(logText, "RegisterBlockDevice(IrqsExhausted)") {
				t.Fatalf("boxlite logs did not contain RegisterBlockDevice(IrqsExhausted)")
			}
			t.Logf("reproduced RegisterBlockDevice(IrqsExhausted); keeping %s for inspection", config.DataRoot)
		})
	}
}

func writeFiveDirBoxLiteManifest(t *testing.T, session *Session) {
	t.Helper()
	writeNDirBoxLiteManifest(t, session, 5)
}

func writeNDirBoxLiteManifest(t *testing.T, session *Session, mountCount int) {
	t.Helper()
	allMounts := []RuntimeMount{
		{HostPath: session.Summary.WorkspacePath, GuestPath: "/workspace", Type: "bind"},
		{HostPath: filepath.Join(HostSessionDir(session), "state"), GuestPath: "/data/state", Type: "bind"},
		{HostPath: filepath.Join(HostSessionDir(session), "runtime"), GuestPath: "/data/runtime", Type: "bind"},
		{HostPath: filepath.Join(HostSessionDir(session), "logs"), GuestPath: "/data/logs", Type: "bind"},
		{HostPath: HostSessionHome(session), GuestPath: "/root", Type: "bind"},
	}
	if mountCount < 0 || mountCount > len(allMounts) {
		t.Fatalf("invalid mount count %d", mountCount)
	}
	manifest := RuntimeMountManifest{
		Version: runtimeMountManifestVersion,
		Driver:  RuntimeDriverBoxlite,
		Mounts:  allMounts[:mountCount],
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal five-dir manifest: %v", err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(runtimeMountManifestPath(session), payload, 0o644); err != nil {
		t.Fatalf("write five-dir manifest: %v", err)
	}
}

func readBoxLiteReproLogs(t *testing.T, boxliteHome string, boxID string) string {
	t.Helper()
	parts := []string{}
	for _, path := range []string{
		filepath.Join(boxliteHome, "boxes", boxID, "shim.stderr"),
		filepath.Join(boxliteHome, "boxes", boxID, "logs", "boxlite-shim.log."+time.Now().Format("2006-01-02")),
		filepath.Join(boxliteHome, "boxes", boxID, "logs", "console.log"),
	} {
		data, err := os.ReadFile(path)
		if err == nil {
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}
