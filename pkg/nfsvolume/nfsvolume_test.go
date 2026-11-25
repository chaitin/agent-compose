package nfsvolume

import (
	"strings"
	"testing"
)

func TestFromOptionsCanonicalizesAndRoundTrips(t *testing.T) {
	cases := []struct {
		name    string
		options map[string]string
		ref     string
		device  string
	}{
		{
			name:    "hostname defaults",
			options: map[string]string{"server": "nas.example.com", "export": "/srv/share"},
			ref:     "nfs://nas.example.com/srv/share",
			device:  "nas.example.com:/srv/share",
		},
		{
			name:    "pinned version and options",
			options: map[string]string{"server": " 10.0.0.5 ", "export": "/data", "version": "4.1", "mount_options": "nolock, timeo=600"},
			ref:     "nfs://10.0.0.5/data?mount_options=nolock%2Ctimeo%3D600&version=4.1",
			device:  "10.0.0.5:/data",
		},
		{
			name:    "bracketed ipv6",
			options: map[string]string{"server": "[fd00::0:1]", "export": "/"},
			ref:     "nfs://[fd00::1]/",
			device:  "[fd00::1]:/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, canonical, err := FromOptions(tc.options)
			if err != nil {
				t.Fatalf("FromOptions: %v", err)
			}
			if got := source.Ref(); got != tc.ref {
				t.Fatalf("Ref = %q, want %q", got, tc.ref)
			}
			if got := source.Device(); got != tc.device {
				t.Fatalf("Device = %q, want %q", got, tc.device)
			}
			parsed, err := ParseRef(source.Ref())
			if err != nil {
				t.Fatalf("ParseRef: %v", err)
			}
			if parsed.Ref() != source.Ref() {
				t.Fatalf("round trip = %q, want %q", parsed.Ref(), source.Ref())
			}
			again, _, err := FromOptions(canonical)
			if err != nil || again.Ref() != source.Ref() {
				t.Fatalf("canonical options do not reproduce the source: %v %q", err, again.Ref())
			}
		})
	}
}

func TestFromOptionsRejectsUnsafeInput(t *testing.T) {
	cases := map[string]map[string]string{
		"missing server":     {"export": "/x"},
		"missing export":     {"server": "nas"},
		"server with port":   {"server": "nas:2049", "export": "/x"},
		"server with space":  {"server": "nas x", "export": "/x"},
		"relative export":    {"server": "nas", "export": "x"},
		"unclean export":     {"server": "nas", "export": "/x/../y"},
		"export with comma":  {"server": "nas", "export": "/x,y"},
		"unknown version":    {"server": "nas", "export": "/x", "version": "2"},
		"reserved addr":      {"server": "nas", "export": "/x", "mount_options": "addr=1.2.3.4"},
		"reserved ro":        {"server": "nas", "export": "/x", "mount_options": "ro"},
		"reserved nfsvers":   {"server": "nas", "export": "/x", "mount_options": "nfsvers=3"},
		"reserved suid":      {"server": "nas", "export": "/x", "mount_options": "suid"},
		"reserved dev":       {"server": "nas", "export": "/x", "mount_options": "dev"},
		"repeated option":    {"server": "nas", "export": "/x", "mount_options": "timeo=1,timeo=2"},
		"option injection":   {"server": "nas", "export": "/x", "mount_options": "soft x"},
		"empty option token": {"server": "nas", "export": "/x", "mount_options": "soft,,hard"},
		"unknown key":        {"server": "nas", "export": "/x", "username": "a"},
	}
	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := FromOptions(options); err == nil {
				t.Fatalf("FromOptions(%v) succeeded", options)
			}
		})
	}
}

func TestParseRefRejectsForeignReferences(t *testing.T) {
	for _, value := range []string{
		"/data/volumes/local/x",
		"default/claim",
		"nfs://nas:2049/x",
		"nfs://user@nas/x",
		"nfs://nas/x?foo=bar",
		"nfs://nas/x?version=4&version=3",
		"nfs://nas/x#frag",
	} {
		if _, err := ParseRef(value); err == nil {
			t.Fatalf("ParseRef(%q) succeeded", value)
		}
	}
	if IsRef("/data/x") || !IsRef("nfs://nas/x") {
		t.Fatal("IsRef misclassified a value")
	}
}

func TestMountSettings(t *testing.T) {
	plain, _, err := FromOptions(map[string]string{"server": "nas", "export": "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.HasCustomMountSettings() {
		t.Fatal("plain export reports custom settings")
	}
	if got := plain.KernelMountData("10.0.0.5"); got != "addr=10.0.0.5,vers=4" {
		t.Fatalf("KernelMountData = %q", got)
	}
	tuned, _, err := FromOptions(map[string]string{"server": "nas", "export": "/x", "version": "3", "mount_options": "nolock"})
	if err != nil {
		t.Fatal(err)
	}
	if !tuned.HasCustomMountSettings() {
		t.Fatal("tuned export does not report custom settings")
	}
	opts := tuned.DockerVolumeOptions()
	if opts["type"] != "nfs" || opts["o"] != "addr=nas,vers=3,nolock,nosuid,nodev" || opts["device"] != ":/x" {
		t.Fatalf("DockerVolumeOptions = %v", opts)
	}
	if plain.DockerVolumeName() == tuned.DockerVolumeName() || !strings.HasPrefix(plain.DockerVolumeName(), "agent-compose-nfs-") {
		t.Fatalf("Docker volume names do not separate mount settings: %q %q", plain.DockerVolumeName(), tuned.DockerVolumeName())
	}
}
