package schema

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	machinesDirName      = "machines"
	kitLockFileName      = "kit.lock"
	retiredHostnamesFile = "retired-hostnames.txt"
	tomlExt              = ".toml"
)

// ValidateFleetDir validates a private fleet directory as one consistent unit,
// matching scripts/validate-fleet.py.
func ValidateFleetDir(fleetRoot, expectedVersion string) ([]Issue, error) {
	machinesDir := filepath.Join(fleetRoot, machinesDirName)
	info, err := os.Stat(machinesDir)
	hasMachines := err == nil && info.IsDir()

	if hasMachines {
		misplaced, err := globTOML(fleetRoot)
		if err != nil {
			return nil, err
		}
		if len(misplaced) > 0 {
			names := make([]string, len(misplaced))
			for i, p := range misplaced {
				names[i] = filepath.Base(p)
			}
			return []Issue{{
				Path:    machinesDirName,
				Message: "machine profiles must be under machines/: " + strings.Join(names, ", "),
			}}, nil
		}
		var nested []string
		walkErr := filepath.WalkDir(machinesDir, func(p string, d fs.DirEntry, wErr error) error {
			if wErr != nil {
				return wErr
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), tomlExt) && filepath.Dir(p) != machinesDir {
				rel, rErr := filepath.Rel(fleetRoot, p)
				if rErr != nil {
					rel = p
				}
				nested = append(nested, filepath.ToSlash(rel))
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk %s: %w", machinesDir, walkErr)
		}
		slices.Sort(nested)
		if len(nested) > 0 {
			return []Issue{{
				Path:    machinesDirName,
				Message: "nested machine profiles are unsupported and would escape the fleet gate: " + strings.Join(nested, ", "),
			}}, nil
		}
	}

	targetDir := fleetRoot
	if hasMachines {
		targetDir = machinesDir
	}
	paths, err := globTOML(targetDir)
	if err != nil {
		return nil, err
	}
	return ValidateFleetPaths(paths, fleetRoot, expectedVersion)
}

// ValidateFleetPaths validates a list of profile paths for fleet-wide
// consistency, matching scripts/validate-fleet.py validate_paths.
func ValidateFleetPaths(paths []string, fleetRoot, expectedVersion string) ([]Issue, error) {
	if len(paths) == 0 {
		return []Issue{{Path: "fleet", Message: "no fleet profiles supplied"}}, nil
	}

	type loadedProfile struct {
		path string
		raw  map[string]any
	}
	var issues []Issue
	var valid []loadedProfile

	for _, p := range paths {
		base := filepath.Base(p)
		raw, err := LoadRawFile(p)
		if err != nil {
			issues = append(issues, Issue{Path: base, Message: err.Error()})
			continue
		}
		pIssues := ValidateRaw(raw, true)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		machine := asTable(raw[SectionMachine.String()])
		host, _ := machine[fieldName(KeyMachineHostname)].(string)
		if stem != host {
			pIssues = append(pIssues, Issue{
				Path:    KeyMachineHostname.String(),
				Message: "must match the profile filename",
			})
		}
		if len(pIssues) > 0 {
			for _, iss := range pIssues {
				issues = append(issues, Issue{Path: base + ":" + iss.Path, Message: iss.Message})
			}
			continue
		}
		valid = append(valid, loadedProfile{path: p, raw: raw})
	}

	hostnames := make([]string, 0, len(valid))
	displayKeys := make([]string, 0, len(valid))
	uuids := make([]string, 0, len(valid))
	assetKeys := make([]string, 0, len(valid))
	var principals []string

	type providerKey struct {
		provider  string
		principal string
	}
	bindings := make(map[providerKey]map[string]bool)

	for _, lp := range valid {
		machine := asTable(lp.raw[SectionMachine.String()])
		accounts := asTable(lp.raw[SectionAccounts.String()])
		scm := asTable(lp.raw[SectionSourceControl.String()])
		collab := asTable(lp.raw[SectionCollaboration.String()])

		host, _ := machine[fieldName(KeyMachineHostname)].(string)
		display, _ := machine[fieldName(KeyMachineDisplayName)].(string)
		uid, _ := machine[fieldName(KeyMachineUUID)].(string)
		asset, _ := machine[fieldName(KeyMachineAssetTag)].(string)

		hostnames = append(hostnames, host)
		displayKeys = append(displayKeys, ComparisonKey(display))
		uuids = append(uuids, strings.ToLower(uid))
		assetKeys = append(assetKeys, ComparisonKey(asset))

		agent, _ := accounts[fieldName(KeyAccountsAgent)].(string)
		principals = append(principals, host+"/"+agent)
		for _, k := range []Key{KeyAccountsHumans, KeyAccountsAdmins, KeyAccountsServices} {
			names, _ := asStringSlice(accounts[fieldName(k)])
			for _, n := range names {
				principals = append(principals, host+"/"+n)
			}
		}

		addBinding := func(provider, principal, def string) {
			if principal == valueNone {
				return
			}
			pk := providerKey{provider: provider, principal: ComparisonKey(principal)}
			if bindings[pk] == nil {
				bindings[pk] = make(map[string]bool)
			}
			bindings[pk][def] = true
		}

		glHost, _ := scm[fieldName(KeySourceControlGitLabHost)].(string)
		glID, _ := scm[fieldName(KeySourceControlGitLabIdentity)].(string)
		glPrincipal, _ := scm[fieldName(KeySourceControlGitLabPrincipal)].(string)
		addBinding("gitlab", glPrincipal, glHost+"|"+glID)

		ghHost, _ := scm[fieldName(KeySourceControlGitHubHost)].(string)
		ghID, _ := scm[fieldName(KeySourceControlGitHubIdentity)].(string)
		ghPrincipal, _ := scm[fieldName(KeySourceControlGitHubPrincipal)].(string)
		addBinding("github", ghPrincipal, ghHost+"|"+ghID)

		atlSite, _ := collab[fieldName(KeyCollaborationAtlassianSite)].(string)
		atlID, _ := collab[fieldName(KeyCollaborationAtlassianIdentity)].(string)
		atlPrincipal, _ := collab[fieldName(KeyCollaborationAtlassianPrincipal)].(string)
		atlAuth, _ := collab[fieldName(KeyCollaborationAtlassianMCPAuth)].(string)
		addBinding("atlassian", atlPrincipal, atlSite+"|"+atlID+"|"+atlAuth)
	}

	for _, dupCheck := range []struct {
		key    Key
		values []string
	}{
		{key: KeyMachineHostname, values: hostnames},
		{key: KeyMachineDisplayName, values: displayKeys},
		{key: KeyMachineUUID, values: uuids},
		{key: KeyMachineAssetTag, values: assetKeys},
	} {
		if repeated := findDuplicates(dupCheck.values); len(repeated) > 0 {
			issues = append(issues, Issue{
				Path:    dupCheck.key.String(),
				Message: fmt.Sprintf("duplicate %s values: %v", dupCheck.key, repeated),
			})
		}
	}
	if repeated := findDuplicates(principals); len(repeated) > 0 {
		issues = append(issues, Issue{
			Path:    SectionAccounts.String(),
			Message: fmt.Sprintf("duplicate host/account principals: %v", repeated),
		})
	}

	bindingKeys := make([]providerKey, 0, len(bindings))
	for k := range bindings {
		bindingKeys = append(bindingKeys, k)
	}
	slices.SortFunc(bindingKeys, func(a, b providerKey) int {
		if a.provider != b.provider {
			return strings.Compare(a.provider, b.provider)
		}
		return strings.Compare(a.principal, b.principal)
	})
	for _, pk := range bindingKeys {
		if len(bindings[pk]) > 1 {
			issues = append(issues, Issue{
				Path:    pk.provider,
				Message: fmt.Sprintf("inconsistent %s principal %q across hosts", pk.provider, pk.principal),
			})
		}
	}

	if fleetRoot != "" {
		issues = append(issues, checkFleetRootMetadata(fleetRoot, expectedVersion, hostnames)...)
	}
	return issues, nil
}

func checkFleetRootMetadata(fleetRoot, expectedVersion string, hostnames []string) []Issue {
	var issues []Issue

	lockPath := filepath.Join(fleetRoot, kitLockFileName)
	lockBytes, err := os.ReadFile(lockPath) //nolint:gosec // caller-supplied fleet root
	if err != nil {
		issues = append(issues, Issue{
			Path:    kitLockFileName,
			Message: "private fleet must pin the toolkit version",
		})
	} else if expectedVersion != "" && strings.TrimSpace(string(lockBytes)) != strings.TrimSpace(expectedVersion) {
		issues = append(issues, Issue{
			Path:    kitLockFileName,
			Message: fmt.Sprintf("expected %s", strings.TrimSpace(expectedVersion)),
		})
	}

	retiredPath := filepath.Join(fleetRoot, retiredHostnamesFile)
	retiredBytes, err := os.ReadFile(retiredPath) //nolint:gosec // caller-supplied fleet root
	if err == nil {
		retired := make(map[string]bool)
		for _, line := range strings.Split(string(retiredBytes), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				retired[trimmed] = true
			}
		}
		if reused := intersectSets(retired, toSet(hostnames)); len(reused) > 0 {
			issues = append(issues, Issue{
				Path:    retiredHostnamesFile,
				Message: fmt.Sprintf("retired hostnames cannot be reused: %v", reused),
			})
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		issues = append(issues, Issue{
			Path:    retiredHostnamesFile,
			Message: fmt.Sprintf("cannot read %s: %v", retiredHostnamesFile, err),
		})
	}
	return issues
}

func globTOML(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), tomlExt) {
			continue
		}
		out = append(out, filepath.Join(dir, entry.Name()))
	}
	slices.Sort(out)
	return out, nil
}

func findDuplicates(values []string) []string {
	counts := make(map[string]int, len(values))
	for _, v := range values {
		counts[v]++
	}
	var repeated []string
	for v, c := range counts {
		if c > 1 {
			repeated = append(repeated, v)
		}
	}
	slices.Sort(repeated)
	return repeated
}
