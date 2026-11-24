package schema

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // Embed IANA timezone data for deterministic validation on minimal hosts.
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	valueAsk  = "ask"
	valueNone = "none"

	classMac   = "mac"
	prefixSvc  = "svc-"
	prefixTag  = "tag:"
	urlScheme  = "://"
	pathSlash  = "/"
	doubleDash = "-"

	msgUnsupportedSelection = "unsupported selection"
	msgHostnameNoURL        = "must be a hostname without a URL scheme/path"

	maxPlainValueLen = 128
	maxEmailLen      = 254
	minPort          = 1
	maxPort          = 65535
	maxReserveFixed  = 1_000_000.0
)

func fieldName(k Key) string {
	if _, name, ok := strings.Cut(k.String(), "."); ok {
		return name
	}
	return k.String()
}

var (
	unixNameRe          = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)
	hostnameRe          = regexp.MustCompile(`^([a-z0-9]{2,8})-(ws|mac|hv|vws|nas|mgmt|srv)-[0-9]{3}$`)
	namespaceRe         = regexp.MustCompile(`^[a-z0-9]{2,8}$`)
	releaseRefRe        = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	displayNameRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
	uuid4Re             = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	agentNameRe         = regexp.MustCompile(`^agent-[0-9]{2}$`)
	adminNameRe         = regexp.MustCompile(`^admin-[0-9]{2}$`)
	principalRe         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,99}$`)
	emailPrincipalRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._%+-]{0,63}@[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	atlassianSvcAcctRe  = regexp.MustCompile(`^[A-Za-z0-9]{6,30}$`)
	aptSpecRe           = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(?::[a-z0-9]+)?(?:=[A-Za-z0-9.+:~_-]+)?$`)
	brewSpecRe          = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]*(?:@[a-z0-9][a-z0-9._-]*)?$`)
	miseVersionRe       = regexp.MustCompile(`^202[6-9]\.[0-9]{1,2}\.[0-9]{1,2}$`)
	miseSHA256Re        = regexp.MustCompile(`^[a-f0-9]{64}$`)
	whitespaceCollapseR = regexp.MustCompile(`\s+`)
)

// LoadRawTOML decodes a TOML document into a raw nested map.
func LoadRawTOML(data []byte) (map[string]any, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("decode toml: %w", err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	return raw, nil
}

// LoadRawFile reads and decodes a TOML file from disk.
func LoadRawFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caller-selected profile path
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	raw, err := LoadRawTOML(data)
	if err != nil {
		return nil, fmt.Errorf("cannot load %s: %w", path, err)
	}
	return raw, nil
}

// ValidateTOML decodes and validates a Schema 3 profile TOML payload.
func ValidateTOML(data []byte, ready bool) ([]Issue, error) {
	raw, err := LoadRawTOML(data)
	if err != nil {
		return nil, err
	}
	return ValidateRaw(raw, ready), nil
}

// ValidateFile reads and validates a Schema 3 profile TOML file.
func ValidateFile(path string, ready bool) ([]Issue, error) {
	raw, err := LoadRawFile(path)
	if err != nil {
		return nil, err
	}
	return ValidateRaw(raw, ready), nil
}

// DecodeProfile validates a Schema 3 TOML payload and decodes it into a typed
// Profile. Invalid profiles fail closed with *ValidationError.
func DecodeProfile(data []byte, ready bool) (Profile, error) {
	issues, err := ValidateTOML(data, ready)
	if err != nil {
		return Profile{}, err
	}
	if len(issues) > 0 {
		return Profile{}, &ValidationError{Issues: issues}
	}
	var p Profile
	if _, err := toml.Decode(string(data), &p); err != nil {
		return Profile{}, fmt.Errorf("decode typed profile: %w", err)
	}
	return p, nil
}

// ComparisonKey normalises a human-assigned identifier for fleet-wide
// comparison, matching Python's NFKC + strip + whitespace collapse + casefold.
func ComparisonKey(value string) string {
	normalised := strings.TrimSpace(norm.NFKC.String(value))
	collapsed := whitespaceCollapseR.ReplaceAllString(normalised, " ")
	return cases.Fold().String(collapsed)
}

// ValidateRaw validates a raw decoded Schema 3 profile map against every
// structural, field, cross-field and readiness rule matching scripts/fleetctl.py.
func ValidateRaw(profile map[string]any, ready bool) []Issue {
	specs := FieldSpecs()
	topAllowed := make(map[string]bool)
	sectionAllowed := make(map[Section]map[string]bool)
	sectionRequired := make(map[Section]map[string]bool)
	var readyKeys []Key

	for _, sec := range AllSections() {
		topAllowed[sec.String()] = true
		sectionAllowed[sec] = make(map[string]bool)
		sectionRequired[sec] = make(map[string]bool)
	}
	for _, fs := range specs {
		if fs.ReadyRequired {
			readyKeys = append(readyKeys, fs.Key)
		}
		if fs.Section == "" {
			topAllowed[fs.Name] = true
			continue
		}
		sectionAllowed[fs.Section][fs.Name] = true
		if fs.Required {
			sectionRequired[fs.Section][fs.Name] = true
		}
	}

	issues := validateStructure(profile, topAllowed, sectionAllowed, sectionRequired)
	if len(issues) > 0 {
		return issues
	}

	deployment := asTable(profile[SectionDeployment.String()])
	machine := asTable(profile[SectionMachine.String()])
	accounts := asTable(profile[SectionAccounts.String()])
	remote := asTable(profile[SectionRemote.String()])
	tooling := asTable(profile[SectionTooling.String()])
	scm := asTable(profile[SectionSourceControl.String()])
	collab := asTable(profile[SectionCollaboration.String()])
	auth := asTable(profile[SectionModelAuth.String()])
	security := asTable(profile[SectionSecurity.String()])
	resources := asTable(profile[SectionResources.String()])
	backup := asTable(profile[SectionBackup.String()])
	maintenance := asTable(profile[SectionMaintenance.String()])

	profileKind, _ := profile[KeyProfile.String()].(string)

	issues = append(issues, validateToolingOptional(tooling, machine)...)
	issues = append(issues, validateMacOSProvisioning(accounts, machine)...)
	issues = append(issues, validateDeployment(deployment, profileKind)...)
	issues = append(issues, validateMachine(machine, deployment)...)
	acctIssues, humans := validateAccounts(accounts)
	issues = append(issues, acctIssues...)
	issues = append(issues, validateRemote(remote)...)
	issues = append(issues, validateToolingCore(tooling)...)
	issues = append(issues, validateSourceControl(scm)...)
	issues = append(issues, validateCollaboration(collab, profileKind, humans)...)
	issues = append(issues, validateModelAuth(auth, profileKind, humans)...)
	issues = append(issues, validateSecurity(security)...)
	issues = append(issues, validateResources(resources)...)
	issues = append(issues, validateBackupAndMaintenance(backup, maintenance)...)

	if ready {
		issues = append(issues, validateReady(profile, readyKeys)...)
	}
	return issues
}

func validateStructure(
	profile map[string]any,
	topAllowed map[string]bool,
	sectionAllowed map[Section]map[string]bool,
	sectionRequired map[Section]map[string]bool,
) []Issue {
	var issues []Issue

	var extraTop []string
	for k := range profile {
		if !topAllowed[k] {
			extraTop = append(extraTop, k)
		}
	}
	slices.Sort(extraTop)
	for _, k := range extraTop {
		issues = append(issues, Issue{Path: k, Message: "unknown top-level key"})
	}

	if v, ok := profile[KeySchemaVersion.String()].(int64); !ok || v != SchemaVersion {
		issues = append(issues, Issue{
			Path:    KeySchemaVersion.String(),
			Message: fmt.Sprintf("must be integer %d", SchemaVersion),
		})
	}
	if s, ok := profile[KeyProfile.String()].(string); !ok || !ProfileKind(s).Valid() {
		issues = append(issues, Issue{Path: KeyProfile.String(), Message: "must be work or personal"})
	}
	if s, ok := profile[KeyState.String()].(string); !ok || !ProfileState(s).Valid() {
		issues = append(issues, Issue{Path: KeyState.String(), Message: "must be draft or approved"})
	}

	for _, sec := range AllSections() {
		secName := sec.String()
		tbl := asTable(profile[secName])
		if tbl == nil {
			issues = append(issues, Issue{Path: secName, Message: "must be a TOML table"})
			continue
		}
		allowed := sectionAllowed[sec]
		required := sectionRequired[sec]

		var unknown []string
		for k := range tbl {
			if !allowed[k] {
				unknown = append(unknown, k)
			}
		}
		slices.Sort(unknown)
		for _, k := range unknown {
			issues = append(issues, Issue{Path: secName + "." + k, Message: "unknown key"})
		}

		var missing []string
		for k := range required {
			if _, exists := tbl[k]; !exists {
				missing = append(missing, k)
			}
		}
		slices.Sort(missing)
		for _, k := range missing {
			issues = append(issues, Issue{Path: secName + "." + k, Message: "required key is missing"})
		}
	}
	return issues
}

func validateToolingOptional(tooling, machine map[string]any) []Issue {
	var issues []Issue
	platformVal, _ := machine[fieldName(KeyMachinePlatform)].(string)

	type pkgRule struct {
		key      Key
		re       *regexp.Regexp
		platform Platform
	}
	pkgRules := []pkgRule{
		{key: KeyToolingAptPackages, re: aptSpecRe, platform: PlatformLinux},
		{key: KeyToolingBrewFormulae, re: brewSpecRe, platform: PlatformMacOS},
		{key: KeyToolingBrewCasks, re: brewSpecRe, platform: PlatformMacOS},
	}
	for _, r := range pkgRules {
		raw, exists := tooling[fieldName(r.key)]
		if !exists {
			continue
		}
		items, ok := asStringSlice(raw)
		if !ok || !allMatch(items, r.re) {
			issues = append(issues, Issue{
				Path:    r.key.String(),
				Message: "must be a list of approved package names (no options, URLs or third-party taps)",
			})
		} else if hasDuplicates(items) {
			issues = append(issues, Issue{Path: r.key.String(), Message: "duplicate package selections"})
		} else if len(items) > 0 && platformVal != r.platform.String() {
			issues = append(issues, Issue{
				Path:    r.key.String(),
				Message: fmt.Sprintf("requires platform %s", r.platform),
			})
		}
	}

	_, hasVer := tooling[fieldName(KeyToolingMiseVersion)]
	_, hasSHA := tooling[fieldName(KeyToolingMiseSHA256)]
	if hasVer || hasSHA {
		ver, verOK := tooling[fieldName(KeyToolingMiseVersion)].(string)
		if !verOK || !miseVersionRe.MatchString(ver) {
			issues = append(issues, Issue{
				Path:    KeyToolingMiseVersion.String(),
				Message: "select an explicit reviewed version with its binary digest",
			})
		}
		sha, shaOK := tooling[fieldName(KeyToolingMiseSHA256)].(string)
		if !shaOK || !miseSHA256Re.MatchString(sha) {
			issues = append(issues, Issue{
				Path:    KeyToolingMiseSHA256.String(),
				Message: "requires the independently reviewed official platform binary SHA-256",
			})
		}
		if platformVal == PlatformMacOS.String() {
			issues = append(issues, Issue{
				Path:    KeyToolingMiseVersion.String(),
				Message: "macOS baseline uses Homebrew mise; do not select a second installation method",
			})
		}
	}
	return issues
}

func validateMacOSProvisioning(accounts, machine map[string]any) []Issue {
	raw, exists := accounts[fieldName(KeyAccountsMacOSProvisioning)]
	if !exists {
		return nil
	}
	platformVal, _ := machine[fieldName(KeyMachinePlatform)].(string)
	val, ok := raw.(string)
	if platformVal != PlatformMacOS.String() || !ok || !MacOSProvisioning(val).Valid() {
		return []Issue{{
			Path:    KeyAccountsMacOSProvisioning.String(),
			Message: "requires macOS and local or managed selection",
		}}
	}
	return nil
}

func validateDeployment(deployment map[string]any, profileKind string) []Issue {
	var issues []Issue
	ns, ok := deployment[fieldName(KeyDeploymentNamespace)].(string)
	if !ok || !namespaceRe.MatchString(ns) {
		issues = append(issues, Issue{
			Path:    KeyDeploymentNamespace.String(),
			Message: "must be 2-8 lowercase letters/digits",
		})
	}
	if ctx, ok := deployment[fieldName(KeyDeploymentContext)].(string); !ok || ctx != profileKind {
		issues = append(issues, Issue{Path: KeyDeploymentContext.String(), Message: "must match profile"})
	}
	own, ownOK := deployment[fieldName(KeyDeploymentOwnership)].(string)
	if !ownOK || !Ownership(own).Valid() {
		issues = append(issues, Issue{
			Path:    KeyDeploymentOwnership.String(),
			Message: fmt.Sprintf("must be %s or %s", OwnershipIndividual, OwnershipOrganization),
		})
	}
	if profileKind == ProfileKindWork.String() && (!ownOK || own != OwnershipOrganization.String()) {
		issues = append(issues, Issue{
			Path:    KeyDeploymentOwnership.String(),
			Message: fmt.Sprintf("work profiles must use ownership %s", OwnershipOrganization),
		})
	}
	return issues
}

func validateMachine(machine, deployment map[string]any) []Issue {
	var issues []Issue
	ns, nsOK := deployment[fieldName(KeyDeploymentNamespace)].(string)

	host, hostOK := machine[fieldName(KeyMachineHostname)].(string)
	var hostMatch []string
	if hostOK {
		hostMatch = hostnameRe.FindStringSubmatch(host)
	}
	if hostMatch == nil {
		issues = append(issues, Issue{
			Path:    KeyMachineHostname.String(),
			Message: "must be <namespace>-<class>-<NNN>, for example acme-ws-001",
		})
	} else if !nsOK || hostMatch[1] != ns {
		issues = append(issues, Issue{
			Path:    KeyMachineHostname.String(),
			Message: "namespace prefix must match deployment.namespace",
		})
	}

	if !validDisplayName(machine[fieldName(KeyMachineDisplayName)]) {
		issues = append(issues, Issue{
			Path:    KeyMachineDisplayName.String(),
			Message: "must be 1-64 trimmed ASCII letters, digits, single spaces, dots, underscores, or hyphens and start with a letter or digit",
		})
	}
	if !validUUID4(machine[fieldName(KeyMachineUUID)]) {
		issues = append(issues, Issue{
			Path:    KeyMachineUUID.String(),
			Message: "must be a canonical UUIDv4 generated once per machine",
		})
	}
	for _, key := range []Key{KeyMachineAssetTag, KeyMachineHardwareProfile, KeyMachineRole} {
		if !validPlainValue(machine[fieldName(key)], maxPlainValueLen) {
			issues = append(issues, Issue{
				Path:    key.String(),
				Message: "must be a canonical, trimmed printable value and cannot start with '-'",
			})
		}
	}

	plat, platOK := machine[fieldName(KeyMachinePlatform)].(string)
	if !platOK || !Platform(plat).Valid() {
		issues = append(issues, Issue{Path: KeyMachinePlatform.String(), Message: "must be linux or macos"})
	}
	var expectedOS string
	if platOK {
		switch Platform(plat) {
		case PlatformLinux:
			expectedOS = OSFamilyUbuntu.String()
		case PlatformMacOS:
			expectedOS = OSFamilyMacOS.String()
		}
	}
	osFam, osOK := machine[fieldName(KeyMachineOSFamily)].(string)
	if !osOK || expectedOS == "" || osFam != expectedOS {
		issues = append(issues, Issue{
			Path:    KeyMachineOSFamily.String(),
			Message: fmt.Sprintf("must be %q for selected platform", expectedOS),
		})
	}
	if hostMatch != nil && platOK {
		if plat == PlatformMacOS.String() && hostMatch[2] != classMac {
			issues = append(issues, Issue{
				Path:    KeyMachineHostname.String(),
				Message: "macOS hosts must use the mac class",
			})
		}
		if plat == PlatformLinux.String() && hostMatch[2] == classMac {
			issues = append(issues, Issue{
				Path:    KeyMachineHostname.String(),
				Message: "Linux hosts cannot use the mac class",
			})
		}
	}
	return issues
}

func validateAccounts(accounts map[string]any) ([]Issue, map[string]bool) {
	var issues []Issue

	agent, agentOK := accounts[fieldName(KeyAccountsAgent)].(string)
	if !agentOK || !unixNameRe.MatchString(agent) {
		issues = append(issues, Issue{Path: KeyAccountsAgent.String(), Message: "must be a valid Unix account name"})
	} else if !agentNameRe.MatchString(agent) {
		issues = append(issues, Issue{Path: KeyAccountsAgent.String(), Message: "must use agent-NN, for example agent-01"})
	}

	lists := make(map[Key][]string, 6)
	for _, key := range []Key{
		KeyAccountsHumans, KeyAccountsAdmins, KeyAccountsServices,
		KeyAccountsOperators, KeyAccountsViewers, KeyAccountsSSHUsers,
	} {
		vals, ok := asStringSlice(accounts[fieldName(key)])
		if !ok {
			issues = append(issues, Issue{Path: key.String(), Message: "must be an array of strings"})
			continue
		}
		lists[key] = vals
		if hasDuplicates(vals) {
			issues = append(issues, Issue{Path: key.String(), Message: "contains duplicate names"})
		}
		for _, v := range vals {
			if !unixNameRe.MatchString(v) {
				issues = append(issues, Issue{Path: key.String(), Message: fmt.Sprintf("invalid Unix name: %s", v)})
			}
		}
	}

	humans := toSet(lists[KeyAccountsHumans])
	admins := toSet(lists[KeyAccountsAdmins])
	services := toSet(lists[KeyAccountsServices])
	humansAndAdmins := unionSets(humans, admins)

	if len(humans) == 0 {
		issues = append(issues, Issue{Path: KeyAccountsHumans.String(), Message: "must contain at least one named human"})
	}
	for a := range admins {
		if !adminNameRe.MatchString(a) {
			issues = append(issues, Issue{Path: KeyAccountsAdmins.String(), Message: "every administrator must use admin-NN"})
			break
		}
	}
	for _, s := range lists[KeyAccountsServices] {
		if !strings.HasPrefix(s, prefixSvc) {
			issues = append(issues, Issue{Path: KeyAccountsServices.String(), Message: "every service account must use svc-purpose"})
			break
		}
	}
	if len(intersectSets(services, humansAndAdmins)) > 0 {
		issues = append(issues, Issue{
			Path:    KeyAccountsServices.String(),
			Message: "service identities must be separate from human/admin accounts",
		})
	}
	for _, key := range []Key{KeyAccountsOperators, KeyAccountsViewers} {
		if extra := diffSets(toSet(lists[key]), humans); len(extra) > 0 {
			issues = append(issues, Issue{
				Path:    key.String(),
				Message: fmt.Sprintf("must be named humans; unexpected: %v", extra),
			})
		}
	}
	sshUsers := toSet(lists[KeyAccountsSSHUsers])
	if extra := diffSets(sshUsers, humansAndAdmins); len(extra) > 0 {
		issues = append(issues, Issue{
			Path:    KeyAccountsSSHUsers.String(),
			Message: fmt.Sprintf("must be declared human/admin accounts: %v", extra),
		})
	}
	if len(lists[KeyAccountsSSHUsers]) == 0 || len(intersectSets(sshUsers, admins)) == 0 {
		issues = append(issues, Issue{
			Path:    KeyAccountsSSHUsers.String(),
			Message: "must include at least one declared administrator for recovery",
		})
	}
	if overlap := intersectSets(humans, admins); len(overlap) > 0 {
		issues = append(issues, Issue{
			Path:    SectionAccounts.String(),
			Message: fmt.Sprintf("daily and administrator accounts must be separate: %v", overlap),
		})
	}
	if agentOK && (humans[agent] || admins[agent] || services[agent]) {
		issues = append(issues, Issue{
			Path:    KeyAccountsAgent.String(),
			Message: "must be separate from every human/admin/service account",
		})
	}

	assignMap, assignOK := asStringMap(accounts[fieldName(KeyAccountsAdminAssignments)])
	if !assignOK {
		issues = append(issues, Issue{
			Path:    KeyAccountsAdminAssignments.String(),
			Message: "must map each admin account to one named human",
		})
	} else {
		assignKeys := make(map[string]bool, len(assignMap))
		owners := make([]string, 0, len(assignMap))
		for k, v := range assignMap {
			assignKeys[k] = true
			owners = append(owners, v)
		}
		if !equalSets(assignKeys, admins) {
			issues = append(issues, Issue{
				Path:    KeyAccountsAdminAssignments.String(),
				Message: "keys must exactly match accounts.admins",
			})
		}
		if unknown := diffSets(toSet(owners), humans); len(unknown) > 0 {
			issues = append(issues, Issue{
				Path:    KeyAccountsAdminAssignments.String(),
				Message: fmt.Sprintf("owners must be declared humans: %v", unknown),
			})
		}
		if hasDuplicates(owners) {
			issues = append(issues, Issue{
				Path:    KeyAccountsAdminAssignments.String(),
				Message: "each admin must be assigned to a different human",
			})
		}
	}

	return issues, humans
}

func validateRemote(remote map[string]any) []Issue {
	var issues []Issue
	if port, ok := remote[fieldName(KeyRemoteNomachinePort)].(int64); !ok || port < minPort || port > maxPort {
		issues = append(issues, Issue{
			Path:    KeyRemoteNomachinePort.String(),
			Message: "must be an integer from 1 through 65535",
		})
	}
	if s, ok := remote[fieldName(KeyRemoteDesktopLockMode)].(string); !ok || !DesktopLockMode(s).Valid() {
		issues = append(issues, Issue{
			Path:    KeyRemoteDesktopLockMode.String(),
			Message: "must be ask, dedicated-shared, or locked",
		})
	}
	if s, ok := remote[fieldName(KeyRemoteKVM)].(string); !ok || !KVMState(s).Valid() {
		issues = append(issues, Issue{
			Path:    KeyRemoteKVM.String(),
			Message: "must be deferred, installed, or not-required",
		})
	}
	for _, key := range []Key{KeyRemotePreferredKVM, KeyRemoteFallbackKVM, KeyRemoteTailscaleTailnet} {
		if s, ok := remote[fieldName(key)].(string); !ok || strings.TrimSpace(s) == "" {
			issues = append(issues, Issue{Path: key.String(), Message: "must be a non-empty inventory choice"})
		}
	}
	tags, tagsOK := asStringSlice(remote[fieldName(KeyRemoteTailscaleTags)])
	tagsValid := tagsOK && len(tags) > 0
	for _, t := range tags {
		if !strings.HasPrefix(t, prefixTag) {
			tagsValid = false
			break
		}
	}
	if !tagsValid {
		issues = append(issues, Issue{
			Path:    KeyRemoteTailscaleTags.String(),
			Message: "must contain at least one tag:* string",
		})
	}
	return issues
}

func validateToolingCore(tooling map[string]any) []Issue {
	var issues []Issue
	if install, ok := tooling[fieldName(KeyToolingInstallAgents)].(bool); !ok || !install {
		issues = append(issues, Issue{
			Path:    KeyToolingInstallAgents.String(),
			Message: "Codex, Claude Code, and Grok Build are required",
		})
	}
	if s, ok := tooling[fieldName(KeyToolingGWS)].(string); !ok || !GWSMode(s).Valid() {
		issues = append(issues, Issue{Path: KeyToolingGWS.String(), Message: "must be ask, install, or skip"})
	}
	if s, ok := tooling[fieldName(KeyToolingSecretsProvider)].(string); !ok || !SecretsProvider(s).Valid() {
		issues = append(issues, Issue{Path: KeyToolingSecretsProvider.String(), Message: msgUnsupportedSelection})
	}
	ref, refOK := tooling[fieldName(KeyToolingAntidoteRef)].(string)
	if !refOK || (ref != valueAsk && (strings.HasPrefix(ref, doubleDash) || !releaseRefRe.MatchString(ref))) {
		issues = append(issues, Issue{
			Path:    KeyToolingAntidoteRef.String(),
			Message: "must be ask or a reviewed release ref",
		})
	}
	return issues
}

func validateSourceControl(scm map[string]any) []Issue {
	var issues []Issue

	glID, glOK := scm[fieldName(KeySourceControlGitLabIdentity)].(string)
	if !glOK || !GitLabIdentity(glID).Valid() {
		issues = append(issues, Issue{
			Path:    KeySourceControlGitLabIdentity.String(),
			Message: "must be service-account or none",
		})
	}
	ghID, ghOK := scm[fieldName(KeySourceControlGitHubIdentity)].(string)
	if !ghOK || !GitHubIdentity(ghID).Valid() {
		issues = append(issues, Issue{
			Path:    KeySourceControlGitHubIdentity.String(),
			Message: "must be app, machine-user, or none",
		})
	}
	if glOK && ghOK && glID == valueNone && ghID == valueNone {
		issues = append(issues, Issue{
			Path:    SectionSourceControl.String(),
			Message: "at least one source-control identity is required",
		})
	}
	for _, key := range []Key{KeySourceControlGitLabHost, KeySourceControlGitHubHost} {
		h, ok := scm[fieldName(key)].(string)
		if !ok || h == "" || strings.Contains(h, urlScheme) || strings.Contains(h, pathSlash) {
			issues = append(issues, Issue{Path: key.String(), Message: msgHostnameNoURL})
		}
	}
	for _, provider := range []struct {
		idKey        Key
		principalKey Key
	}{
		{idKey: KeySourceControlGitLabIdentity, principalKey: KeySourceControlGitLabPrincipal},
		{idKey: KeySourceControlGitHubIdentity, principalKey: KeySourceControlGitHubPrincipal},
	} {
		idVal, idIsStr := scm[fieldName(provider.idKey)].(string)
		principalRaw := scm[fieldName(provider.principalKey)]
		principalStr, principalIsStr := principalRaw.(string)
		if idIsStr && idVal == valueNone && (!principalIsStr || principalStr != valueNone) {
			issues = append(issues, Issue{
				Path:    provider.principalKey.String(),
				Message: "must be none when the provider identity is none",
			})
		}
		if (!idIsStr || idVal != valueNone) && ((principalIsStr && principalStr == valueNone) || !validPrincipal(principalRaw)) {
			issues = append(issues, Issue{
				Path:    provider.principalKey.String(),
				Message: "must be an approved non-secret principal label, not none or a URL",
			})
		}
	}
	return issues
}

func validateCollaboration(collab map[string]any, profileKind string, humans map[string]bool) []Issue {
	var issues []Issue

	atlID, atlIDOK := collab[fieldName(KeyCollaborationAtlassianIdentity)].(string)
	if !atlIDOK || !AtlassianIdentity(atlID).Valid() {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianIdentity.String(),
			Message: "must be ask, service-account, named-human, or none",
		})
	}
	mcpAuth, mcpAuthOK := collab[fieldName(KeyCollaborationAtlassianMCPAuth)].(string)
	if !mcpAuthOK || !AtlassianMCPAuth(mcpAuth).Valid() {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianMCPAuth.String(),
			Message: "unsupported Atlassian MCP authentication mode",
		})
	}
	if profileKind == ProfileKindWork.String() && atlIDOK && atlID == AtlassianIdentityNamedHuman.String() {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianIdentity.String(),
			Message: "a shared work agent cannot use a named-human Atlassian identity",
		})
	}
	if len(humans) > 1 && atlIDOK && atlID == AtlassianIdentityNamedHuman.String() {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianIdentity.String(),
			Message: "a multi-operator agent cannot use one person's Atlassian identity",
		})
	}
	if atlIDOK && atlID == AtlassianIdentityServiceAccount.String() && (!mcpAuthOK || mcpAuth != AtlassianMCPAuthServiceAccountAPIKey.String()) {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianMCPAuth.String(),
			Message: "a service account must use service-account-api-key",
		})
	}
	if atlIDOK && atlID == AtlassianIdentityNamedHuman.String() && (!mcpAuthOK || mcpAuth != AtlassianMCPAuthOAuth21.String()) {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianMCPAuth.String(),
			Message: "a named human must use oauth-2.1",
		})
	}

	principalRaw := collab[fieldName(KeyCollaborationAtlassianPrincipal)]
	principalStr, principalIsStr := principalRaw.(string)
	if atlIDOK && atlID == valueNone && (!principalIsStr || principalStr != valueNone || !mcpAuthOK || mcpAuth != valueNone) {
		issues = append(issues, Issue{
			Path:    SectionCollaboration.String(),
			Message: "an unused Atlassian integration must set principal and MCP auth to none",
		})
	}

	var principalOK bool
	switch {
	case atlIDOK && atlID == AtlassianIdentityNamedHuman.String():
		principalOK = validNamedHumanPrincipal(principalRaw)
	case atlIDOK && atlID == AtlassianIdentityServiceAccount.String():
		principalOK = validPrincipal(principalRaw) && atlassianSvcAcctRe.MatchString(principalStr)
	default:
		principalOK = validPrincipal(principalRaw)
	}
	if (!atlIDOK || (atlID != valueAsk && atlID != valueNone)) && ((principalIsStr && principalStr == valueNone) || !principalOK) {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianPrincipal.String(),
			Message: "must be a 6-30 character alphanumeric service-account name or, for named-human, an approved label/email address",
		})
	}

	siteRaw := collab[fieldName(KeyCollaborationAtlassianSite)]
	siteStr, siteIsStr := siteRaw.(string)
	if (!siteIsStr || (siteStr != valueAsk && siteStr != valueNone)) && (!siteIsStr || siteStr == "" || strings.Contains(siteStr, urlScheme) || strings.Contains(siteStr, pathSlash)) {
		issues = append(issues, Issue{Path: KeyCollaborationAtlassianSite.String(), Message: msgHostnameNoURL})
	}
	if atlIDOK && atlID == valueNone && (!siteIsStr || siteStr != valueNone) {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianSite.String(),
			Message: "must be none when Atlassian is unused",
		})
	}
	if (!atlIDOK || (atlID != valueAsk && atlID != valueNone)) && siteIsStr && (siteStr == valueAsk || siteStr == valueNone) {
		issues = append(issues, Issue{
			Path:    KeyCollaborationAtlassianSite.String(),
			Message: "an enabled Atlassian identity requires a site hostname",
		})
	}
	return issues
}

func validateModelAuth(auth map[string]any, profileKind string, humans map[string]bool) []Issue {
	var issues []Issue
	for _, key := range []Key{KeyModelAuthCodex, KeyModelAuthClaude, KeyModelAuthGrok} {
		val, ok := auth[fieldName(key)].(string)
		if !ok || !ModelAuthMode(val).Valid() {
			issues = append(issues, Issue{Path: key.String(), Message: "unsupported authentication mode"})
		}
		if profileKind == ProfileKindWork.String() && ok && val == ModelAuthModeNamedHuman.String() {
			issues = append(issues, Issue{
				Path:    key.String(),
				Message: "work shared homes require a workload/enterprise identity",
			})
		}
		if len(humans) > 1 && ok && val == ModelAuthModeNamedHuman.String() {
			issues = append(issues, Issue{
				Path:    key.String(),
				Message: "a personal identity cannot be shared by multiple operators",
			})
		}
	}
	return issues
}

func validateSecurity(security map[string]any) []Issue {
	var issues []Issue
	for _, key := range []Key{KeySecurityDiskEncryptionRequired, KeySecuritySecureBootRequired} {
		if b, ok := security[fieldName(key)].(bool); !ok || !b {
			issues = append(issues, Issue{Path: key.String(), Message: "must be true for this baseline"})
		}
	}
	if s, ok := security[fieldName(KeySecurityRemoteScope)].(string); !ok || s != RemoteScopeTailscaleOnly.String() {
		issues = append(issues, Issue{Path: KeySecurityRemoteScope.String(), Message: "must be tailscale-only"})
	}
	if s, ok := security[fieldName(KeySecurityEndpointManagement)].(string); !ok || !EndpointManagement(s).Valid() {
		issues = append(issues, Issue{Path: KeySecurityEndpointManagement.String(), Message: msgUnsupportedSelection})
	}
	return issues
}

func validateResources(resources map[string]any) []Issue {
	var issues []Issue
	if s, ok := resources[fieldName(KeyResourcesPolicy)].(string); !ok || s != ResourcePolicyMeasuredBalanced.String() {
		issues = append(issues, Issue{Path: KeyResourcesPolicy.String(), Message: "must be measured-balanced"})
	}
	for _, bound := range []struct {
		key   Key
		lower int64
		upper int64
	}{
		{key: KeyResourcesOSMemoryReservePercent, lower: 5, upper: 50},
		{key: KeyResourcesOSMemoryReserveGiB, lower: 4, upper: 32},
		{key: KeyResourcesOSCPUReserveThreads, lower: 1, upper: 8},
	} {
		raw, exists := resources[fieldName(bound.key)]
		if !exists {
			continue
		}
		if v, ok := raw.(int64); !ok || v < bound.lower || v > bound.upper {
			issues = append(issues, Issue{
				Path:    bound.key.String(),
				Message: fmt.Sprintf("must be an integer from %d through %d", bound.lower, bound.upper),
			})
		}
	}
	if raw, exists := resources[fieldName(KeyResourcesReserveFixedGiB)]; exists {
		num, ok := asFiniteNumber(raw)
		if !ok || num <= 0 || num >= maxReserveFixed {
			issues = append(issues, Issue{
				Path:    KeyResourcesReserveFixedGiB.String(),
				Message: "must be a positive number below 1000000",
			})
		}
		if _, hasPct := resources[fieldName(KeyResourcesOSMemoryReservePercent)]; hasPct {
			issues = append(issues, Issue{
				Path:    KeyResourcesReserveFixedGiB.String(),
				Message: "cannot be combined with resources.os_memory_reserve_percent",
			})
		}
	}
	if raw, exists := resources[fieldName(KeyResourcesReserveCores)]; exists {
		if v, ok := raw.(int64); !ok || v < 0 || v > 1024 {
			issues = append(issues, Issue{
				Path:    KeyResourcesReserveCores.String(),
				Message: "must be an integer from 0 through 1024",
			})
		}
	}
	if raw, exists := resources[fieldName(KeyResourcesCPUWeight)]; exists {
		if v, ok := raw.(int64); !ok || v < 1 || v > 10000 {
			issues = append(issues, Issue{
				Path:    KeyResourcesCPUWeight.String(),
				Message: "must be an integer from 1 through 10000",
			})
		}
	}
	return issues
}

func validateBackupAndMaintenance(backup, maintenance map[string]any) []Issue {
	var issues []Issue
	if days, ok := backup[fieldName(KeyBackupRetentionDays)].(int64); !ok || days < 1 {
		issues = append(issues, Issue{Path: KeyBackupRetentionDays.String(), Message: "must be a positive integer"})
	}
	for _, item := range []struct {
		key   Key
		table map[string]any
	}{
		{key: KeyMaintenanceTimezone, table: maintenance},
		{key: KeyMaintenanceUpdateWindow, table: maintenance},
		{key: KeyMaintenanceOwner, table: maintenance},
		{key: KeyBackupTarget, table: backup},
	} {
		if s, ok := item.table[fieldName(item.key)].(string); !ok || strings.TrimSpace(s) == "" {
			issues = append(issues, Issue{Path: item.key.String(), Message: "must be a non-empty string"})
		}
	}
	if tz, ok := maintenance[fieldName(KeyMaintenanceTimezone)].(string); ok && strings.TrimSpace(tz) != "" {
		if !validIANATimezone(tz) {
			issues = append(issues, Issue{
				Path:    KeyMaintenanceTimezone.String(),
				Message: "must be a recognised IANA timezone",
			})
		}
	}
	return issues
}

func validateReady(profile map[string]any, readyKeys []Key) []Issue {
	var issues []Issue
	if s, ok := profile[KeyState.String()].(string); !ok || s != ProfileStateApproved.String() {
		issues = append(issues, Issue{Path: KeyState.String(), Message: "must be approved before apply/audit"})
	}
	sorted := make([]string, len(readyKeys))
	for i, k := range readyKeys {
		sorted[i] = k.String()
	}
	slices.Sort(sorted)
	for _, p := range sorted {
		if v, ok := nestedValue(profile, p); ok {
			if s, isStr := v.(string); isStr && s == valueAsk {
				issues = append(issues, Issue{Path: p, Message: "must be resolved before apply/audit"})
			}
		}
	}
	return issues
}

func validDisplayName(raw any) bool {
	s, ok := raw.(string)
	if !ok {
		return false
	}
	return s == strings.TrimSpace(s) && !strings.Contains(s, "  ") && displayNameRe.MatchString(s)
}

func validUUID4(raw any) bool {
	s, ok := raw.(string)
	return ok && uuid4Re.MatchString(s)
}

func validPlainValue(raw any, maxLen int) bool {
	s, ok := raw.(string)
	if !ok {
		return false
	}
	if s != strings.TrimSpace(s) || strings.HasPrefix(s, doubleDash) {
		return false
	}
	runeCount := utf8.RuneCountInString(s)
	if runeCount < 1 || runeCount > maxLen {
		return false
	}
	if !norm.NFKC.IsNormalString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.Is(unicode.C, r) {
			return false
		}
	}
	return true
}

func looksLikeCredential(value string) bool {
	lowered := cases.Fold().String(value)
	for _, prefix := range []string{
		"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
		"glpat-", "glptt-", "glcbt-", "gldt-", "glrt-", "atatt",
		"sk-", "sk-ant-", "xai-",
	} {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

func validPrincipal(raw any) bool {
	s, ok := raw.(string)
	return ok && principalRe.MatchString(s) && !looksLikeCredential(s)
}

func validNamedHumanPrincipal(raw any) bool {
	if validPrincipal(raw) {
		return true
	}
	s, ok := raw.(string)
	if !ok || utf8.RuneCountInString(s) > maxEmailLen || looksLikeCredential(s) {
		return false
	}
	return emailPrincipalRe.MatchString(s)
}

func validIANATimezone(tz string) bool {
	if tz == "" || tz == "Local" || strings.HasPrefix(tz, pathSlash) || strings.Contains(tz, "..") || strings.Contains(tz, "\\") {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

func nestedValue(doc map[string]any, dotted string) (any, bool) {
	var cur any = doc
	for _, part := range strings.Split(dotted, ".") {
		tbl := asTable(cur)
		if tbl == nil {
			return nil, false
		}
		v, ok := tbl[part]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

func asTable(v any) map[string]any {
	tbl, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return tbl
}

func asStringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return slices.Clone(s), true
	case []any:
		out := make([]string, len(s))
		for i, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = str
		}
		return out, true
	default:
		return nil, false
	}
}

func asStringMap(v any) (map[string]string, bool) {
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out, true
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, rawVal := range m {
			str, ok := rawVal.(string)
			if !ok {
				return nil, false
			}
			out[k] = str
		}
		return out, true
	default:
		return nil, false
	}
}

func asFiniteNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func allMatch(items []string, re *regexp.Regexp) bool {
	for _, item := range items {
		if !re.MatchString(item) {
			return false
		}
	}
	return true
}

func hasDuplicates(items []string) bool {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if seen[item] {
			return true
		}
		seen[item] = true
	}
	return false
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}

func unionSets(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func intersectSets(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if b[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func diffSets(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func equalSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// RenderTOML encodes a raw configuration map as canonical TOML.
func RenderTOML(doc map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return nil, fmt.Errorf("encode toml: %w", err)
	}
	return buf.Bytes(), nil
}
