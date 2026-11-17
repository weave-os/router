package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"weave-os/router/internal/router/hmm"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: validate-roster <roster.json>")
		os.Exit(2)
	}
	ok, err := run(os.Args[1], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func run(path string, out io.Writer) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	arms, err := rosterArms(data)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	diagnostics := hmm.ValidateRosterIDs(arms)
	for _, d := range diagnostics {
		if _, err := fmt.Fprintf(out, "invalid roster arm %q: %s\n", d.RosterID, d.Reason); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprintf(out, "validated %d roster arms: %d invalid\n", len(arms), len(diagnostics)); err != nil {
		return false, err
	}
	return len(diagnostics) == 0, nil
}

func rosterArms(data []byte) ([]string, error) {
	var flat []string
	if err := json.Unmarshal(data, &flat); err == nil {
		return flat, nil
	}
	var doc struct {
		RosterIDs []string                   `json:"roster_ids"`
		Clusters  map[string]clusterArmsList `json:"clusters"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.RosterIDs) > 0 {
		return doc.RosterIDs, nil
	}
	seen := make(map[string]struct{})
	var arms []string
	for _, cluster := range doc.Clusters {
		for _, arm := range cluster {
			if _, dup := seen[arm]; dup {
				continue
			}
			seen[arm] = struct{}{}
			arms = append(arms, arm)
		}
	}
	if len(arms) == 0 {
		return nil, fmt.Errorf("no roster arms found (expected a JSON array, \"roster_ids\", or \"clusters\")")
	}
	return arms, nil
}

type clusterArmsList []string

func (c *clusterArmsList) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*c = list
		return nil
	}
	var obj struct {
		Arms []string `json:"arms"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	*c = obj.Arms
	return nil
}
