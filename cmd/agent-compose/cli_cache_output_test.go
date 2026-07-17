package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestCLIImageCacheAndFilterHelpersCoverEdgeBranches(t *testing.T) {
	image := testCLIImage("sha256:1234567890abcdef", "agent:latest")
	image.AvailabilityStatus = agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_ERROR
	image.Platform = &agentcomposev2.ImagePlatform{Os: "linux", Architecture: "amd64", Variant: "v8"}
	image.Labels = map[string]string{"k": "v"}
	pull := composeImagePullOutputFromResponse(&agentcomposev2.PullImageResponse{
		Image:       image,
		ResolvedRef: "agent@sha256:1234",
		Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_FAILED,
		Progress: []*agentcomposev2.ImagePullProgress{{
			Id: "layer", Status: "done", Progress: "1/1", CurrentBytes: 1, TotalBytes: 1,
		}},
		Warnings: []string{"already exists; skipped"},
	})
	if pull.Status != "failed" || pull.Image.Platform != "linux/amd64/v8" || !imagePullSkipped(pull) {
		t.Fatalf("pull output = %#v", pull)
	}
	var text bytes.Buffer
	if err := writeImagePullText(&text, pull); err != nil {
		t.Fatalf("writeImagePullText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Skipped") || !strings.Contains(text.String(), "Warning") {
		t.Fatalf("pull text = %q", text.String())
	}
	listOutput := composeImageListOutputFromResponse(&agentcomposev2.ListImagesResponse{
		Images:     []*agentcomposev2.Image{image},
		TotalCount: 1,
		HasMore:    true,
		NextOffset: 25,
		StoreStatus: &agentcomposev2.ImageStoreStatus{
			Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
			Available: true,
			Endpoint:  "/tmp/images",
		},
	})
	if len(listOutput.Images) != 1 || !listOutput.HasMore || listOutput.StoreStatus.Store != "oci-cache" {
		t.Fatalf("image list output = %#v", listOutput)
	}
	inspectOutput := composeImageInspectOutputFromResponse(&agentcomposev2.InspectImageResponse{
		Image:       image,
		StoreStatus: &agentcomposev2.ImageStoreStatus{Store: agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON, Error: "down"},
	})
	if inspectOutput.Image.ImageID == "" || inspectOutput.StoreStatus.Store != "docker" || inspectOutput.StoreStatus.Error != "down" {
		t.Fatalf("image inspect output = %#v", inspectOutput)
	}
	removeOutput := composeImageRemoveOutputFromResponse(&agentcomposev2.RemoveImageResponse{
		ImageRef: "agent:old", UntaggedRefs: []string{"agent:old"}, DeletedIds: []string{"sha256:old"}, Warnings: []string{"warn"},
	})
	if removeOutput.ImageRef != "agent:old" || len(removeOutput.UntaggedRefs) != 1 || len(removeOutput.DeletedIDs) != 1 {
		t.Fatalf("image remove output = %#v", removeOutput)
	}
	text.Reset()
	if err := writeImagesText(&text, listOutput.Images, false); err != nil {
		t.Fatalf("writeImagesText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "IMAGE ID") || !strings.Contains(text.String(), "REF") || !strings.Contains(text.String(), "DISK USAGE") || !strings.Contains(text.String(), "agent:latest") || !strings.Contains(text.String(), "1.0KB") || strings.Contains(text.String(), "CONTENT SIZE") {
		t.Fatalf("images text = %q", text.String())
	}
	text.Reset()
	untaggedImage := composeImageOutput{
		ImageID:          "sha256:7e31c0c15f55c1c4bc9ccbd8d435987df0893c71f1ecdb324e87df0bc77e1c2a",
		ShortID:          "7e31c0c15f55",
		ImageRef:         "sha256:7e31c0c15f55c1c4bc9ccbd8d435987df0893c71f1ecdb324e87df0bc77e1c2a",
		ResolvedRef:      "example.com/agent@sha256:7e31c0c15f55c1c4bc9ccbd8d435987df0893c71f1ecdb324e87df0bc77e1c2a",
		SizeBytes:        559279329,
		VirtualSizeBytes: 559279329,
	}
	if err := writeImagesText(&text, []composeImageOutput{untaggedImage}, false); err != nil {
		t.Fatalf("writeImagesText untagged returned error: %v", err)
	}
	if !strings.Contains(text.String(), "<none>") || strings.Contains(text.String(), "example.com/agent@sha256") || strings.Contains(text.String(), "sha256:7e31c0c15f55") {
		t.Fatalf("untagged images text = %q", text.String())
	}
	text.Reset()
	if err := writeImagesText(&text, listOutput.Images, true); err != nil {
		t.Fatalf("writeImagesText verbose returned error: %v", err)
	}
	if !strings.Contains(text.String(), "STORE") || !strings.Contains(text.String(), "STATUS") || !strings.Contains(text.String(), "CONTENT SIZE") || !strings.Contains(text.String(), "CREATED") {
		t.Fatalf("images verbose text = %q", text.String())
	}
	if shortImageID("sha256:1234567890abcdef") != "1234567890ab" || imagePlatformText(&agentcomposev2.ImagePlatform{Os: "linux"}) != "linux" {
		t.Fatalf("image helper output mismatch")
	}
	if formatImageSizeForText(0) != "0B" || formatImageSizeForText(559279329) != "559.3MB" || firstNonZeroUint64(0, 42) != 42 {
		t.Fatalf("image text helper output mismatch")
	}
	if formatImageCreatedForText("") != "-" || formatImageCreatedForText("created") != "created" || formatImageAgeForText(2*time.Hour) != "2 hours" {
		t.Fatalf("image time helper output mismatch")
	}
	if imageListRefForText(untaggedImage) != "<none>" || imageRefLooksUntagged("example.com/agent@sha256:def", "") != true || imageRefLooksUntagged("agent:latest", "") {
		t.Fatalf("image ref text helper output mismatch")
	}

	cache := composeCacheOutputFromProto(testCLICache("cache-full"))
	if cache.ID != "cache-full" || cache.Domain == "" || cache.Type == "" || cacheRefText(cache) == "-" {
		t.Fatalf("cache output = %#v", cache)
	}
	emptyCache := composeCacheOutputFromProto(nil)
	if emptyCache.ID != "" {
		t.Fatalf("nil cache output = %#v", emptyCache)
	}
	cacheListOutput := composeCacheListOutputFromResponse(&agentcomposev2.ListCachesResponse{
		Caches:   []*agentcomposev2.CacheItem{testCLICache("cache-list")},
		Warnings: []string{"cache warning"},
	})
	if len(cacheListOutput.Caches) != 1 || len(cacheListOutput.Warnings) != 1 {
		t.Fatalf("cache list output = %#v", cacheListOutput)
	}
	cacheInspectOutput := composeCacheInspectOutputFromResponse(&agentcomposev2.InspectCacheResponse{
		Cache:    testCLICache("cache-inspect"),
		Warnings: []string{"inspect warning"},
	})
	if cacheInspectOutput.Cache.ID != "cache-inspect" || len(cacheInspectOutput.Warnings) != 1 {
		t.Fatalf("cache inspect output = %#v", cacheInspectOutput)
	}
	pruneOutput := composeCacheOperationOutputFromPruneResponse(&agentcomposev2.PruneCachesResponse{
		DryRun: true,
		Matched: []*agentcomposev2.CacheItem{
			testCLICache("cache-match"),
		},
		Skipped:  []*agentcomposev2.CacheItem{testCLICache("cache-skip")},
		Removed:  []string{"cache-old"},
		Warnings: []string{"prune warning"},
	})
	removeCacheOutput := composeCacheOperationOutputFromRemoveResponse(&agentcomposev2.RemoveCacheResponse{
		Matched: []*agentcomposev2.CacheItem{testCLICache("cache-remove")},
		Skipped: []*agentcomposev2.CacheItem{testCLICache("cache-remove-skip")},
		Removed: []string{"cache-remove"},
	})
	if len(pruneOutput.Matched) != 1 || len(pruneOutput.Skipped) != 1 || len(removeCacheOutput.Removed) != 1 {
		t.Fatalf("cache operation outputs prune=%#v remove=%#v", pruneOutput, removeCacheOutput)
	}
	text.Reset()
	if err := writeCacheListText(&text, cacheListOutput); err != nil {
		t.Fatalf("writeCacheListText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "CACHE ID") || !strings.Contains(text.String(), "cache-list") || !strings.Contains(text.String(), "Warnings") {
		t.Fatalf("cache list text = %q", text.String())
	}
	text.Reset()
	if err := writeCacheInspectText(&text, composeCacheInspectOutput{Cache: cache, Warnings: []string{"top warning"}}); err != nil {
		t.Fatalf("writeCacheInspectText returned error: %v", err)
	}
	for _, want := range []string{"Cache ID", "Image:", "Last used:", "References:", "Warnings:"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("cache inspect text %q missing %q", text.String(), want)
		}
	}
	text.Reset()
	if err := writeCacheOperationOutput(&text, false, composeCacheOperationOutput{
		DryRun:   true,
		Matched:  []composeCacheOutput{cache},
		Skipped:  []composeCacheOutput{{ID: "cache-skip", ShortID: "cache-skip", Driver: "docker", Type: "oci", Status: "active", BlockedReasons: []string{"in use"}}},
		Warnings: []string{"warning"},
	}); err != nil {
		t.Fatalf("writeCacheOperationOutput text returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Dry-run") || !strings.Contains(text.String(), "Skipped") || !strings.Contains(text.String(), "in use") {
		t.Fatalf("cache operation text = %q", text.String())
	}
	text.Reset()
	if err := writeCacheOperationOutput(&text, true, composeCacheOperationOutput{Removed: []string{"cache-full"}}); err != nil {
		t.Fatalf("writeCacheOperationOutput JSON returned error: %v", err)
	}
	if !strings.Contains(text.String(), `"removed"`) {
		t.Fatalf("cache operation json = %q", text.String())
	}
	text.Reset()
	if err := writeCacheOperationOutput(&text, false, composeCacheOperationOutput{
		Matched: []composeCacheOutput{cache},
		Removed: []string{
			"cache-full",
		},
	}); err != nil {
		t.Fatalf("writeCacheOperationOutput removed text returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Removed 1 cache") || !strings.Contains(text.String(), "Matched") {
		t.Fatalf("cache operation removed text = %q", text.String())
	}

	text.Reset()
	if err := writeSandboxPruneOutput(&text, false, composeSandboxPruneOutput{
		DryRun:   true,
		Matched:  []composePSSandboxOutput{{SandboxID: "sandbox-1", SandboxShortID: "sandbox-1", Agent: "reviewer", Status: "stopped", Driver: "boxlite", CreatedAt: "created"}},
		Skipped:  []composeSandboxPruneSkipped{{SandboxID: "sandbox-2", Reason: "running"}},
		Warnings: []string{"warning"},
	}); err != nil {
		t.Fatalf("writeSandboxPruneOutput returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Use --force") || !strings.Contains(text.String(), "would remove") {
		t.Fatalf("sandbox prune text = %q", text.String())
	}
	text.Reset()
	if err := writeSandboxPruneOutput(&text, true, composeSandboxPruneOutput{Removed: []string{"sandbox-removed"}}); err != nil {
		t.Fatalf("writeSandboxPruneOutput JSON returned error: %v", err)
	}
	if !strings.Contains(text.String(), "sandbox-removed") {
		t.Fatalf("sandbox prune json = %q", text.String())
	}
	text.Reset()
	if err := writeSandboxPruneOutput(&text, false, composeSandboxPruneOutput{
		Matched: []composePSSandboxOutput{{SandboxID: "sandbox-3", SandboxShortID: "sandbox-3", Agent: "worker", Status: "stopped", Driver: "docker", UpdatedAt: "updated"}},
		Removed: []string{"sandbox-3"},
	}); err != nil {
		t.Fatalf("writeSandboxPruneOutput removed returned error: %v", err)
	}
	if !strings.Contains(text.String(), "Removed 1 sandbox") || !strings.Contains(text.String(), "sandbox-3") {
		t.Fatalf("sandbox prune removed text = %q", text.String())
	}

	for _, value := range []string{"linux/amd64", "linux/amd64/v8"} {
		platform, err := parseImagePlatform(value)
		if err != nil || platform.GetOs() != "linux" || platform.GetArchitecture() != "amd64" {
			t.Fatalf("parseImagePlatform(%q) = %#v err=%v", value, platform, err)
		}
	}
	if _, err := parseImagePlatform("linux"); err == nil || !strings.Contains(err.Error(), "expected os/arch") {
		t.Fatalf("parseImagePlatform invalid error = %v", err)
	}
	if platform, err := parseImagePlatform(""); err != nil || platform != nil {
		t.Fatalf("parseImagePlatform empty = %#v err=%v", platform, err)
	}
	if filter, err := cacheFilterFromOptions(composeCacheFilterOptions{}); err != nil || filter != nil {
		t.Fatalf("empty cache filter = %#v err=%v", filter, err)
	}
	if filter, err := cacheFilterFromOptions(composeCacheFilterOptions{Driver: "all", Type: "skill", Status: "referenced"}); err != nil || filter.GetDriver() != "all" || filter.GetType() != "skill" || filter.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED {
		t.Fatalf("cache filter = %#v err=%v", filter, err)
	}
	if filter, err := cacheFilterFromPruneOptions(composeCachePruneOptions{OlderThan: "2h"}); err != nil || filter.GetOlderThanSeconds() != 7200 {
		t.Fatalf("prune filter = %#v err=%v", filter, err)
	}
	for _, tc := range []struct {
		options composeCachePruneOptions
		want    agentcomposev2.CacheStatus
	}{
		{options: composeCachePruneOptions{Unused: true}, want: agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED},
		{options: composeCachePruneOptions{Orphaned: true}, want: agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED},
		{options: composeCachePruneOptions{Expired: true}, want: agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED},
	} {
		filter, err := cacheFilterFromPruneOptions(tc.options)
		if err != nil || filter.GetStatus() != tc.want {
			t.Fatalf("shortcut prune filter = %#v err=%v want %v", filter, err, tc.want)
		}
	}
	for _, tc := range []struct {
		options composeCachePruneOptions
		want    string
	}{
		{options: composeCachePruneOptions{Unused: true, Orphaned: true}, want: "mutually exclusive"},
		{options: composeCachePruneOptions{Unused: true, composeCacheFilterOptions: composeCacheFilterOptions{Status: "active"}}, want: "cannot be combined"},
		{options: composeCachePruneOptions{OlderThan: "bad"}, want: "invalid --older-than"},
		{options: composeCachePruneOptions{OlderThan: "0s"}, want: "positive"},
		{options: composeCachePruneOptions{OlderThan: "500ms"}, want: "at least 1s"},
	} {
		if _, err := cacheFilterFromPruneOptions(tc.options); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("cacheFilterFromPruneOptions(%#v) error = %v, want %q", tc.options, err, tc.want)
		}
	}
	for _, tc := range []struct {
		options composeCacheFilterOptions
		want    string
	}{
		{options: composeCacheFilterOptions{Driver: "bad"}, want: "invalid --driver"},
		{options: composeCacheFilterOptions{Type: "bad"}, want: "invalid --type"},
		{options: composeCacheFilterOptions{Status: "bad"}, want: "invalid --status"},
	} {
		if _, err := cacheFilterFromOptions(tc.options); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("cacheFilterFromOptions(%#v) error = %v, want %q", tc.options, err, tc.want)
		}
	}
	if cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_UNSPECIFIED) != "unspecified" ||
		cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE) != "oci-image-store" ||
		cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE) != "runtime-derived-cache" ||
		cacheTypeText(agentcomposev2.CacheDomain_CACHE_DOMAIN_SKILL_ARTIFACT_CACHE) != "skill" ||
		cacheTypeText(agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE) != "oci" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE) != "active" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED) != "referenced" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED) != "unused" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED) != "expired" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN) != "unknown" ||
		imageStoreText(agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_UNSPECIFIED) != "unspecified" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_MISSING) != "missing" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE) != "available" ||
		imageOperationStatusText(agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED) != "succeeded" {
		t.Fatalf("status text helper mismatch")
	}
	for _, tc := range []struct {
		status agentcomposev2.RunStatus
		text   string
	}{
		{agentcomposev2.RunStatus_RUN_STATUS_PENDING, "pending"},
		{agentcomposev2.RunStatus_RUN_STATUS_RUNNING, "running"},
		{agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, "succeeded"},
		{agentcomposev2.RunStatus_RUN_STATUS_FAILED, "failed"},
		{agentcomposev2.RunStatus_RUN_STATUS_CANCELED, "canceled"},
		{agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED, "unspecified"},
	} {
		if got := runStatusText(tc.status); got != tc.text {
			t.Fatalf("runStatusText(%v) = %q, want %q", tc.status, got, tc.text)
		}
	}
	for _, tc := range []struct {
		source agentcomposev2.RunSource
		text   string
	}{
		{agentcomposev2.RunSource_RUN_SOURCE_MANUAL, "manual"},
		{agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER, "scheduler"},
		{agentcomposev2.RunSource_RUN_SOURCE_API, "api"},
		{agentcomposev2.RunSource_RUN_SOURCE_UNSPECIFIED, "unspecified"},
	} {
		if got := runSourceText(tc.source); got != tc.text {
			t.Fatalf("runSourceText(%v) = %q, want %q", tc.source, got, tc.text)
		}
	}
	if firstNonEmptyString("", " value ") != " value " || firstNonEmptyString(" ", "") != "" {
		t.Fatalf("firstNonEmptyString returned unexpected value")
	}
	quoted := shellQuoteCLIArg("it's complicated")
	if quoted != "'it'\"'\"'s complicated'" || shellQuoteCLIArg("") != "''" || shellQuoteCLIArg("plain") != "plain" {
		t.Fatalf("shellQuoteCLIArg returned %q", quoted)
	}
	unique := appendUniqueStrings([]string{"a", " "}, "b", "a", " c ")
	if strings.Join(unique, ",") != "a,b,c" {
		t.Fatalf("appendUniqueStrings = %#v", unique)
	}
}
