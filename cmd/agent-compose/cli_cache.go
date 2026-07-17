package main

import (
	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type composeCacheFilterOptions struct {
	Driver string
	Type   string
	Status string
}

type composeCachePruneOptions struct {
	composeCacheFilterOptions
	Unused    bool
	Orphaned  bool
	Expired   bool
	OlderThan string
	Force     bool
}

type composeCacheRemoveOptions struct {
	Force bool
}

func addCacheFilterFlags(cmd *cobra.Command, options *composeCacheFilterOptions) {
	cmd.Flags().StringVar(&options.Driver, "driver", "", "Filter caches by driver: docker, boxlite, microsandbox, or all")
	cmd.Flags().StringVar(&options.Type, "type", "", "Filter caches by type: oci, materialized, runtime, or skill")
	cmd.Flags().StringVar(&options.Status, "status", "", "Filter caches by status: active, referenced, unused, expired, orphaned, or unknown")
}

func addCachePruneFlags(cmd *cobra.Command, options *composeCachePruneOptions) {
	addCacheFilterFlags(cmd, &options.composeCacheFilterOptions)
	cmd.Flags().BoolVar(&options.Unused, "unused", false, "Only match unused caches")
	cmd.Flags().BoolVar(&options.Orphaned, "orphaned", false, "Only match orphaned caches")
	cmd.Flags().BoolVar(&options.Expired, "expired", false, "Only match expired caches")
	cmd.Flags().StringVar(&options.OlderThan, "older-than", "", "Only match caches older than a duration such as 7d or 24h")
	cmd.Flags().BoolVar(&options.Force, "force", false, "Actually remove matched caches")
}

func addCacheRemoveFlags(cmd *cobra.Command, options *composeCacheRemoveOptions) {
	cmd.Flags().BoolVar(&options.Force, "force", false, "Actually remove the cache item")
}

func cacheInspectArgs(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache inspect accepts 1 arg(s), received %d", len(args))}
	}
	return nil
}

func cacheRemoveArgs(_ *cobra.Command, args []string) error {
	if len(args) != 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache rm accepts 1 arg(s), received %d", len(args))}
	}
	return nil
}

func runComposeCacheListCommand(cmd *cobra.Command, cli cliOptions, options composeCacheFilterOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	filter, err := cacheFilterFromOptions(options)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	resp, err := clients.cache.ListCaches(cmd.Context(), connect.NewRequest(&agentcomposev2.ListCachesRequest{
		Filter: filter,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list caches: %w", err))
	}
	output := composeCacheListOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeCacheListText(cmd.OutOrStdout(), output)
}

func runComposeCacheInspectCommand(cmd *cobra.Command, cli cliOptions, cacheID string) error {
	cacheID = strings.TrimSpace(cacheID)
	if cacheID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache inspect requires a cache id")}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.cache.InspectCache(cmd.Context(), connect.NewRequest(&agentcomposev2.InspectCacheRequest{
		CacheId: cacheID,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("inspect cache %s: %w", cacheID, err))
	}
	output := composeCacheInspectOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeCacheInspectText(cmd.OutOrStdout(), output)
}

func runComposeCachePruneCommand(cmd *cobra.Command, cli cliOptions, options composeCachePruneOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	filter, err := cacheFilterFromPruneOptions(options)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	resp, err := clients.cache.PruneCaches(cmd.Context(), connect.NewRequest(&agentcomposev2.PruneCachesRequest{
		Filter: filter,
		Force:  options.Force,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("prune caches: %w", err))
	}
	output := composeCacheOperationOutputFromPruneResponse(resp.Msg)
	if err := writeCacheOperationOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	return nil
}

func runComposeCacheRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeCacheRemoveOptions, cacheID string) error {
	cacheID = strings.TrimSpace(cacheID)
	if cacheID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("cache rm requires a cache id")}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.cache.RemoveCache(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveCacheRequest{
		CacheId: cacheID,
		Force:   options.Force,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("remove cache %s: %w", cacheID, err))
	}
	output := composeCacheOperationOutputFromRemoveResponse(resp.Msg)
	if err := writeCacheOperationOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	if options.Force && len(output.Removed) == 0 && len(output.Skipped) > 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("remove cache %s: %s", cacheID, cacheRemoveFailureReason(cacheID, output))}
	}
	return nil
}

func cacheRemoveFailureReason(cacheID string, output composeCacheOperationOutput) string {
	for _, skipped := range output.Skipped {
		if skipped.ID != cacheID {
			continue
		}
		if cacheStringListContains(skipped.BlockedReasons, "remove failed") {
			if warning := firstCacheRemoveWarning(cacheID, output.Warnings); warning != "" {
				return warning
			}
			return "remove failed"
		}
		if len(skipped.BlockedReasons) > 0 {
			return strings.Join(skipped.BlockedReasons, "; ")
		}
		if len(skipped.Warnings) > 0 {
			return strings.Join(skipped.Warnings, "; ")
		}
	}
	if warning := firstCacheRemoveWarning(cacheID, output.Warnings); warning != "" {
		return warning
	}
	if len(output.Warnings) > 0 {
		return output.Warnings[0]
	}
	return "cache is protected"
}

func firstCacheRemoveWarning(cacheID string, warnings []string) string {
	for _, warning := range warnings {
		if strings.Contains(warning, cacheID) {
			return warning
		}
	}
	return ""
}

func cacheStringListContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type composeCacheListOutput struct {
	Caches   []composeCacheOutput `json:"caches"`
	Warnings []string             `json:"warnings,omitempty"`
}

type composeCacheInspectOutput struct {
	Cache    composeCacheOutput `json:"cache"`
	Warnings []string           `json:"warnings,omitempty"`
}

type composeCacheOperationOutput struct {
	DryRun   bool                 `json:"dry_run"`
	Matched  []composeCacheOutput `json:"matched"`
	Removed  []string             `json:"removed"`
	Skipped  []composeCacheOutput `json:"skipped"`
	Warnings []string             `json:"warnings,omitempty"`
}

type composeCacheOutput struct {
	ID             string                        `json:"id"`
	ShortID        string                        `json:"short_id"`
	Domain         string                        `json:"domain"`
	Type           string                        `json:"type"`
	Driver         string                        `json:"driver"`
	Kind           string                        `json:"kind"`
	Path           string                        `json:"path,omitempty"`
	SizeBytes      uint64                        `json:"size_bytes"`
	ImageID        string                        `json:"image_id,omitempty"`
	ImageRef       string                        `json:"image_ref,omitempty"`
	ResolvedRef    string                        `json:"resolved_ref,omitempty"`
	Status         string                        `json:"status"`
	Removable      bool                          `json:"removable"`
	BlockedReasons []string                      `json:"blocked_reasons,omitempty"`
	LastUsedAt     string                        `json:"last_used_at,omitempty"`
	LastUsedSource string                        `json:"last_used_source,omitempty"`
	References     []composeCacheReferenceOutput `json:"references,omitempty"`
	Warnings       []string                      `json:"warnings,omitempty"`
}

type composeCacheReferenceOutput struct {
	Policy      string `json:"policy,omitempty"`
	Type        string `json:"type,omitempty"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Path        string `json:"path,omitempty"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

func composeCacheListOutputFromResponse(resp *agentcomposev2.ListCachesResponse) composeCacheListOutput {
	output := composeCacheListOutput{
		Caches:   make([]composeCacheOutput, 0, len(resp.GetCaches())),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
	for _, cache := range resp.GetCaches() {
		output.Caches = append(output.Caches, composeCacheOutputFromProto(cache))
	}
	return output
}

func composeCacheInspectOutputFromResponse(resp *agentcomposev2.InspectCacheResponse) composeCacheInspectOutput {
	return composeCacheInspectOutput{
		Cache:    composeCacheOutputFromProto(resp.GetCache()),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
}

func composeCacheOperationOutputFromPruneResponse(resp *agentcomposev2.PruneCachesResponse) composeCacheOperationOutput {
	output := composeCacheOperationOutput{
		DryRun:   resp.GetDryRun(),
		Matched:  make([]composeCacheOutput, 0, len(resp.GetMatched())),
		Removed:  displayOpaqueIDs(resp.GetRemoved()),
		Skipped:  make([]composeCacheOutput, 0, len(resp.GetSkipped())),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
	for _, cache := range resp.GetMatched() {
		output.Matched = append(output.Matched, composeCacheOutputFromProto(cache))
	}
	for _, cache := range resp.GetSkipped() {
		output.Skipped = append(output.Skipped, composeCacheOutputFromProto(cache))
	}
	return output
}

func composeCacheOperationOutputFromRemoveResponse(resp *agentcomposev2.RemoveCacheResponse) composeCacheOperationOutput {
	output := composeCacheOperationOutput{
		DryRun:   resp.GetDryRun(),
		Matched:  make([]composeCacheOutput, 0, len(resp.GetMatched())),
		Removed:  displayOpaqueIDs(resp.GetRemoved()),
		Skipped:  make([]composeCacheOutput, 0, len(resp.GetSkipped())),
		Warnings: append([]string(nil), resp.GetWarnings()...),
	}
	for _, cache := range resp.GetMatched() {
		output.Matched = append(output.Matched, composeCacheOutputFromProto(cache))
	}
	for _, cache := range resp.GetSkipped() {
		output.Skipped = append(output.Skipped, composeCacheOutputFromProto(cache))
	}
	return output
}

func composeCacheOutputFromProto(cache *agentcomposev2.CacheItem) composeCacheOutput {
	if cache == nil {
		return composeCacheOutput{}
	}
	refs := make([]composeCacheReferenceOutput, 0, len(cache.GetReferences()))
	for _, ref := range cache.GetReferences() {
		refs = append(refs, composeCacheReferenceOutput{
			Policy:      cacheReferencePolicyText(ref.GetPolicy()),
			Type:        ref.GetType(),
			ID:          displayOpaqueID(ref.GetId()),
			Name:        ref.GetName(),
			Path:        ref.GetPath(),
			Status:      ref.GetStatus(),
			Description: ref.GetDescription(),
		})
	}
	return composeCacheOutput{
		ID:             displayOpaqueID(cache.GetCacheId()),
		ShortID:        identity.ShortID(cache.GetCacheId()),
		Domain:         cacheDomainText(cache.GetDomain()),
		Type:           cacheTypeText(cache.GetDomain()),
		Driver:         cache.GetDriver(),
		Kind:           cache.GetKind(),
		Path:           cache.GetPath(),
		SizeBytes:      cache.GetSizeBytes(),
		ImageID:        displayOpaqueID(cache.GetImageId()),
		ImageRef:       cache.GetImageRef(),
		ResolvedRef:    cache.GetResolvedRef(),
		Status:         cacheStatusText(cache.GetStatus()),
		Removable:      cache.GetRemovable(),
		BlockedReasons: append([]string(nil), cache.GetBlockedReasons()...),
		LastUsedAt:     cache.GetLastUsedAt(),
		LastUsedSource: cache.GetLastUsedSource(),
		References:     refs,
		Warnings:       append([]string(nil), cache.GetWarnings()...),
	}
}

func writeCacheListText(out io.Writer, output composeCacheListOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CACHE ID\tDRIVER\tTYPE\tSTATUS\tREMOVABLE\tSIZE\tREF\tPATH"); err != nil {
		return err
	}
	for _, cache := range output.Caches {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			firstNonEmptyString(cache.ShortID, shortOpaqueID(cache.ID), "-"),
			firstNonEmptyString(cache.Driver, "-"),
			firstNonEmptyString(cache.Type, "-"),
			firstNonEmptyString(cache.Status, "-"),
			strconv.FormatBool(cache.Removable),
			cache.SizeBytes,
			cacheRefText(cache),
			firstNonEmptyString(cache.Path, "-"),
		); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writeStringListSection(out, "Warnings", output.Warnings)
}

func writeCacheInspectText(out io.Writer, output composeCacheInspectOutput) error {
	cache := output.Cache
	if _, err := fmt.Fprintf(out, "Cache ID: %s\nDomain: %s\nType: %s\nDriver: %s\nKind: %s\nStatus: %s\nRemovable: %t\nSize: %d\nPath: %s\n",
		firstNonEmptyString(cache.ID, "-"),
		firstNonEmptyString(cache.Domain, "-"),
		firstNonEmptyString(cache.Type, "-"),
		firstNonEmptyString(cache.Driver, "-"),
		firstNonEmptyString(cache.Kind, "-"),
		firstNonEmptyString(cache.Status, "-"),
		cache.Removable,
		cache.SizeBytes,
		firstNonEmptyString(cache.Path, "-"),
	); err != nil {
		return err
	}
	if cache.ImageID != "" || cache.ImageRef != "" || cache.ResolvedRef != "" {
		if _, err := fmt.Fprintf(out, "Image: %s\nResolved: %s\nImage ID: %s\n",
			firstNonEmptyString(cache.ImageRef, "-"),
			firstNonEmptyString(cache.ResolvedRef, "-"),
			firstNonEmptyString(cache.ImageID, "-"),
		); err != nil {
			return err
		}
	}
	if cache.LastUsedAt != "" || cache.LastUsedSource != "" {
		if _, err := fmt.Fprintf(out, "Last used: %s (%s)\n",
			firstNonEmptyString(cache.LastUsedAt, "-"),
			firstNonEmptyString(cache.LastUsedSource, "-"),
		); err != nil {
			return err
		}
	}
	if err := writeStringListSection(out, "Blocked reasons", cache.BlockedReasons); err != nil {
		return err
	}
	if err := writeCacheReferencesSection(out, cache.References); err != nil {
		return err
	}
	if err := writeStringListSection(out, "Warnings", append(append([]string(nil), output.Warnings...), cache.Warnings...)); err != nil {
		return err
	}
	return nil
}

func writeCacheOperationOutput(out io.Writer, asJSON bool, output composeCacheOperationOutput) error {
	if asJSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	if output.DryRun {
		if _, err := fmt.Fprintf(out, "Dry-run: %d matched, %d skipped, %d would be removed.\n", len(output.Matched), len(output.Skipped), len(output.Matched)-len(output.Skipped)); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "Removed %d cache item(s); %d matched, %d skipped.\n", len(output.Removed), len(output.Matched), len(output.Skipped)); err != nil {
			return err
		}
	}
	if len(output.Removed) > 0 {
		if err := writeStringListSection(out, "Removed", output.Removed); err != nil {
			return err
		}
	}
	if len(output.Matched) > 0 {
		if _, err := fmt.Fprintln(out, "Matched:"); err != nil {
			return err
		}
		if err := writeCacheOperationTable(out, output.Matched); err != nil {
			return err
		}
	}
	if len(output.Skipped) > 0 {
		if _, err := fmt.Fprintln(out, "Skipped:"); err != nil {
			return err
		}
		if err := writeCacheOperationTable(out, output.Skipped); err != nil {
			return err
		}
	}
	return writeStringListSection(out, "Warnings", output.Warnings)
}

func writeCacheOperationTable(out io.Writer, caches []composeCacheOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CACHE ID\tDRIVER\tTYPE\tSTATUS\tREMOVABLE\tREASON"); err != nil {
		return err
	}
	for _, cache := range caches {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(cache.ID, "-"),
			firstNonEmptyString(cache.Driver, "-"),
			firstNonEmptyString(cache.Type, "-"),
			firstNonEmptyString(cache.Status, "-"),
			strconv.FormatBool(cache.Removable),
			firstNonEmptyString(strings.Join(cache.BlockedReasons, "; "), "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeCacheReferencesSection(out io.Writer, refs []composeCacheReferenceOutput) error {
	if len(refs) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(out, "References:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "TYPE\tID\tNAME\tSTATUS\tPATH\tDESCRIPTION"); err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(ref.Type, "-"),
			firstNonEmptyString(ref.ID, "-"),
			firstNonEmptyString(ref.Name, "-"),
			firstNonEmptyString(ref.Status, "-"),
			firstNonEmptyString(ref.Path, "-"),
			firstNonEmptyString(ref.Description, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func cacheFilterFromOptions(options composeCacheFilterOptions) (*agentcomposev2.CacheFilter, error) {
	driver, err := cacheDriverFilterValue(options.Driver)
	if err != nil {
		return nil, err
	}
	cacheType, err := cacheTypeFilterValue(options.Type)
	if err != nil {
		return nil, err
	}
	status, err := cacheStatusFilterValue(options.Status)
	if err != nil {
		return nil, err
	}
	if driver == "" && cacheType == "" && status == agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED {
		return nil, nil
	}
	return &agentcomposev2.CacheFilter{
		Driver: driver,
		Type:   cacheType,
		Status: status,
	}, nil
}

func cacheFilterFromPruneOptions(options composeCachePruneOptions) (*agentcomposev2.CacheFilter, error) {
	base, err := cacheFilterFromOptions(options.composeCacheFilterOptions)
	if err != nil {
		return nil, err
	}
	status, err := cachePruneShortcutStatus(options)
	if err != nil {
		return nil, err
	}
	if status != agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED {
		if base == nil {
			base = &agentcomposev2.CacheFilter{}
		}
		base.Status = status
	}
	if strings.TrimSpace(options.OlderThan) != "" {
		seconds, err := parseOlderThanSeconds(options.OlderThan)
		if err != nil {
			return nil, err
		}
		if base == nil {
			base = &agentcomposev2.CacheFilter{}
		}
		base.OlderThanSeconds = seconds
	}
	return base, nil
}

func cachePruneShortcutStatus(options composeCachePruneOptions) (agentcomposev2.CacheStatus, error) {
	var selected []string
	var status agentcomposev2.CacheStatus
	if options.Unused {
		selected = append(selected, "--unused")
		status = agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED
	}
	if options.Orphaned {
		selected = append(selected, "--orphaned")
		status = agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED
	}
	if options.Expired {
		selected = append(selected, "--expired")
		status = agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED
	}
	if len(selected) > 1 {
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, fmt.Errorf("%s are mutually exclusive", strings.Join(selected, ", "))
	}
	if len(selected) == 1 && strings.TrimSpace(options.Status) != "" {
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, fmt.Errorf("%s cannot be combined with --status", selected[0])
	}
	return status, nil
}

func cacheDriverFilterValue(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "docker", "boxlite", "microsandbox", "all":
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("invalid --driver %q: expected docker, boxlite, microsandbox, or all", value)
	}
}

func cacheTypeFilterValue(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "oci", "materialized", "runtime", "skill":
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("invalid --type %q: expected oci, materialized, runtime, or skill", value)
	}
}

func cacheStatusFilterValue(value string) (agentcomposev2.CacheStatus, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, nil
	case "active":
		return agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE, nil
	case "referenced":
		return agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED, nil
	case "unused":
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED, nil
	case "expired":
		return agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED, nil
	case "orphaned":
		return agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED, nil
	case "unknown":
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN, nil
	default:
		return agentcomposev2.CacheStatus_CACHE_STATUS_UNSPECIFIED, fmt.Errorf("invalid --status %q: expected active, referenced, unused, expired, orphaned, or unknown", value)
	}
}

func cacheDomainText(domain agentcomposev2.CacheDomain) string {
	switch domain {
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE:
		return "oci-image-store"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE:
		return "materialized-image-cache"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE:
		return "runtime-derived-cache"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_SKILL_ARTIFACT_CACHE:
		return "skill-artifact-cache"
	default:
		return "unspecified"
	}
}

func cacheTypeText(domain agentcomposev2.CacheDomain) string {
	switch domain {
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE:
		return "oci"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE:
		return "materialized"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE:
		return "runtime"
	case agentcomposev2.CacheDomain_CACHE_DOMAIN_SKILL_ARTIFACT_CACHE:
		return "skill"
	default:
		return "unspecified"
	}
}

func cacheStatusText(status agentcomposev2.CacheStatus) string {
	switch status {
	case agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE:
		return "active"
	case agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED:
		return "referenced"
	case agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED:
		return "unused"
	case agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED:
		return "expired"
	case agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED:
		return "orphaned"
	case agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN:
		return "unknown"
	default:
		return "unspecified"
	}
}

func cacheRefText(cache composeCacheOutput) string {
	if cache.ImageRef != "" || cache.ResolvedRef != "" {
		return firstNonEmptyString(cache.ImageRef, cache.ResolvedRef)
	}
	if cache.ImageID != "" {
		return shortImageID(cache.ImageID)
	}
	return "-"
}

func cacheReferencePolicyText(policy agentcomposev2.CacheReferencePolicy) string {
	if policy == agentcomposev2.CacheReferencePolicy_CACHE_REFERENCE_POLICY_ADVISORY {
		return "advisory"
	}
	return "required"
}
