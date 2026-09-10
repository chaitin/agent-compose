package driver

import (
	"fmt"
	"reflect"
	"sync"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func TestWorkspaceMountSnapshotReadersPreserveConcurrentConfig(t *testing.T) {
	for _, configured := range []bool{false, true} {
		for _, reader := range []string{"spec", "manifest"} {
			t.Run(fmt.Sprintf("configured=%v/%s", configured, reader), func(t *testing.T) {
				sandbox, source := testWorkspaceMountSandbox(t, "reference", true)
				config := &appconfig.Config{}
				if configured {
					config = testRuntimeMountConfig()
				}
				config.GuestHomePath = "/caller-owned-home"
				before := *config
				setupConfig := *config
				manifest, err := prepareRuntimeMountManifest(&setupConfig, sandbox, RuntimeDriverDocker)
				if err != nil {
					t.Fatal(err)
				}
				mount, err := decodeSandboxWorkspaceMount(sandbox, RuntimeDriverDocker)
				if err != nil {
					t.Fatal(err)
				}
				want := &runtimeMountSpec{hostPath: source, guestPath: "/workspace/reference", readOnly: true, mustExist: true}
				read := func() error {
					if reader == "manifest" {
						return validateWorkspaceRuntimeManifest(config, sandbox, manifest, want)
					}
					got, err := workspaceRuntimeMountSpecFromSnapshot(config, sandbox, mount)
					if err != nil {
						return err
					}
					if !reflect.DeepEqual(got, want) {
						return fmt.Errorf("snapshot spec = %#v, want %#v", got, want)
					}
					return nil
				}
				if err := read(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(*config, before) {
					t.Fatal("snapshot reader changed caller-owned config")
				}
				start := make(chan struct{})
				var group sync.WaitGroup
				for range 8 {
					group.Go(func() {
						<-start
						for range 32 {
							if err := read(); err != nil {
								t.Error(err)
								return
							}
						}
					})
				}
				close(start)
				group.Wait()
				if !reflect.DeepEqual(*config, before) {
					t.Fatal("concurrent snapshot readers changed caller-owned config")
				}
			})
		}
	}
}
