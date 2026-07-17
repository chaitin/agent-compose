package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type composeDisplayChangeBuilder struct {
	items     []composeDisplayChangeOutput
	seenKey   map[string]int
	projectID string
}

func newComposeDisplayChangeBuilder() *composeDisplayChangeBuilder {
	return &composeDisplayChangeBuilder{
		seenKey: make(map[string]int),
	}
}

func composeDisplayChangeKey(change composeDisplayChangeOutput) string {
	if change.ResourceType == "trigger" && change.Owner != "" && change.Name != "" {
		return change.ResourceType + "\x00" + change.Owner + "\x00" + change.Name
	}
	if change.ResourceType == "agent" && change.Name != "" {
		return change.ResourceType + "\x00" + change.Name
	}
	identity := change.ID
	if identity == "" {
		identity = change.Name
	}
	return change.ResourceType + "\x00" + identity
}

func mergeComposeDisplayChange(existing, next composeDisplayChangeOutput) composeDisplayChangeOutput {
	if projectChangeActionRank(next.Action) > projectChangeActionRank(existing.Action) {
		if strings.TrimSpace(next.Message) == "" {
			next.Message = existing.Message
		}
		return next
	}
	if existing.Message == "" {
		existing.Message = next.Message
	}
	return existing
}

func composePSStatusFilter(options composePSOptions) (map[string]bool, error) {
	values := strings.Split(options.Status, ",")
	result := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		result[value] = true
	}
	if len(result) > 0 {
		return result, nil
	}
	if options.All {
		return nil, nil
	}
	return map[string]bool{"running": true}, nil
}

func writePSText(out io.Writer, output composePSOutput, verbose bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if verbose {
		if _, err := fmt.Fprintln(tw, "SANDBOX ID\tAGENT\tSTATUS\tRUN ID\tCREATED\tUPDATED\tDRIVER\tIMAGE\tWORKSPACE"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(tw, "SANDBOX ID\tAGENT\tSTATUS\tRUN ID\tCREATED\tUPDATED"); err != nil {
		return err
	}
	for _, sandbox := range output.Sandboxes {
		if verbose {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				firstNonEmptyString(sandbox.SandboxID, "-"),
				firstNonEmptyString(sandbox.Agent, "-"),
				firstNonEmptyString(sandbox.Status, "-"),
				firstNonEmptyString(sandbox.RunID, "-"),
				firstNonEmptyString(sandbox.CreatedAt, "-"),
				firstNonEmptyString(sandbox.UpdatedAt, "-"),
				firstNonEmptyString(sandbox.Driver, "-"),
				firstNonEmptyString(sandbox.Image, "-"),
				firstNonEmptyString(sandbox.Workspace, "-"),
			); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(sandbox.SandboxShortID, shortOpaqueID(sandbox.SandboxID), "-"),
			firstNonEmptyString(sandbox.Agent, "-"),
			firstNonEmptyString(sandbox.Status, "-"),
			firstNonEmptyString(sandbox.RunShortID, shortOpaqueID(sandbox.RunID), "-"),
			firstNonEmptyString(sandbox.CreatedAt, "-"),
			firstNonEmptyString(sandbox.UpdatedAt, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}
