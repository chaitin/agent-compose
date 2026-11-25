//go:build linux

package driver

import "testing"

func TestParseNFSMountStateUsesTopMount(t *testing.T) {
	mountinfo := `22 1 0:21 / /data rw,relatime shared:1 - ext4 /dev/vda1 rw
90 22 0:50 / /data/sandboxes/s1/volumes/mount-a rw,nosuid,nodev - nfs4 nas:/srv rw,vers=4.2,addr=10.0.0.5
91 22 0:51 / /data/sandboxes/s1/volumes/mount\040b rw - nfs nas:/x rw,vers=3
92 22 0:51 / /data/sandboxes/s1/volumes/mount-c rw - nfs4 nas:/x rw
93 92 0:52 / /data/sandboxes/s1/volumes/mount-c rw - tmpfs tmpfs rw
`
	cases := []struct {
		path           string
		mounted, isNFS bool
	}{
		{"/data/sandboxes/s1/volumes/mount-a", true, true},
		{"/data/sandboxes/s1/volumes/mount-a/", true, true},
		{"/data/sandboxes/s1/volumes/mount b", true, true},
		{"/data/sandboxes/s1/volumes/mount-c", true, false},
		{"/data/sandboxes/s1/volumes/missing", false, false},
		{"/data", true, false},
	}
	for _, tc := range cases {
		mounted, isNFS, err := parseNFSMountState(mountinfo, tc.path)
		if err != nil || mounted != tc.mounted || isNFS != tc.isNFS {
			t.Fatalf("parseNFSMountState(%q) = %v, %v, %v; want %v, %v", tc.path, mounted, isNFS, err, tc.mounted, tc.isNFS)
		}
	}
}
