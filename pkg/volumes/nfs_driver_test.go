package volumes

import (
	"context"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/nfsvolume"
)

func TestManagerCreatesAndResolvesNFSVolume(t *testing.T) {
	store := &fakeStore{}
	manager := NewManager(store, NewNFSDriver())
	created, err := manager.Create(context.Background(), domain.VolumeRecord{
		Name:    "shared",
		Driver:  "NFS",
		Options: map[string]string{"server": "nas.example.com", "export": "/srv/share", "mount_options": "nolock"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Driver != domain.VolumeDriverNFS || created.Path != "nfs://nas.example.com/srv/share?mount_options=nolock" {
		t.Fatalf("created = %+v", created)
	}
	if created.Options["server"] != "nas.example.com" || created.Options["mount_options"] != "nolock" {
		t.Fatalf("canonical options = %v", created.Options)
	}

	mounts, _, err := manager.ResolveMounts(context.Background(), []domain.VolumeMountSpec{
		{Type: domain.VolumeMountTypeVolume, Source: "shared", Target: "/mnt/shared", ReadOnly: true},
	}, ResolveOptions{})
	if err != nil {
		t.Fatalf("ResolveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].Driver != domain.VolumeDriverNFS || mounts[0].HostPath != created.Path || !mounts[0].ReadOnly {
		t.Fatalf("mounts = %+v", mounts)
	}

	if _, err := manager.Inspect(context.Background(), "shared"); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	listed, err := manager.List(context.Background(), domain.VolumeListOptions{Driver: "nfs"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %v, %v", listed, err)
	}
	if err := manager.Remove(context.Background(), "shared", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestManagerRejectsInvalidNFSVolume(t *testing.T) {
	manager := NewManager(&fakeStore{}, NewNFSDriver())
	_, err := manager.Create(context.Background(), domain.VolumeRecord{
		Name:    "shared",
		Driver:  "nfs",
		Options: map[string]string{"server": "nas", "export": "/x", "password": "secret"},
	})
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("Create error = %v", err)
	}
}

func TestNFSDriverRejectsCorruptedRecordPath(t *testing.T) {
	driver := NewNFSDriver()
	record := domain.VolumeRecord{Name: "shared", Driver: "nfs", Path: "/data/volumes/local/x"}
	if _, err := driver.ResolveMountSource(context.Background(), record); err == nil {
		t.Fatal("ResolveMountSource accepted a host path")
	}
	if _, err := driver.Inspect(context.Background(), record); err == nil {
		t.Fatal("Inspect accepted a host path")
	}
}

func TestValidateResolvedDriverMountsForNFSOnK8s(t *testing.T) {
	plain, _, err := nfsvolume.FromOptions(map[string]string{"server": "nas", "export": "/x"})
	if err != nil {
		t.Fatal(err)
	}
	tuned, _, err := nfsvolume.FromOptions(map[string]string{"server": "nas", "export": "/x", "version": "4.1"})
	if err != nil {
		t.Fatal(err)
	}
	mount := func(ref string) []domain.SandboxVolumeMount {
		return []domain.SandboxVolumeMount{{Type: "volume", Source: "shared", Target: "/mnt", Driver: "nfs", HostPath: ref}}
	}
	if err := ValidateResolvedDriverMounts("k8s", mount(plain.Ref())); err != nil {
		t.Fatalf("plain nfs on k8s: %v", err)
	}
	if err := ValidateResolvedDriverMounts("k8s", mount(tuned.Ref())); err == nil || !strings.Contains(err.Error(), "version or mount_options") {
		t.Fatalf("tuned nfs on k8s error = %v", err)
	}
	if err := ValidateResolvedDriverMounts("docker", mount(tuned.Ref())); err != nil {
		t.Fatalf("tuned nfs on docker: %v", err)
	}
}
