package driver

// GuestDirectoryPublication describes a private guest directory publication.
// ManagedMarker identifies a legacy directory that this caller owns.
type GuestDirectoryPublication struct {
	HostSource       string
	GuestDestination string
	ManagedMarker    string
}

// GuestSymlinkProjection describes an alias inside the private guest home.
// ManagedMarkers name caller-owned legacy formats; unrelated paths are refused.
type GuestSymlinkProjection struct {
	GuestPath      string
	RelativeTarget string
	ManagedMarkers []string
}
