package policy

func servedRosterVersion(sidecarRosterVersion, pinnedServedRosterSHA256, configuredSelectionPolicySHA256 string, pinned bool) string {
	switch {
	case pinned:
		return pinnedServedRosterSHA256
	case configuredSelectionPolicySHA256 != "":
		return configuredSelectionPolicySHA256
	default:
		return sidecarRosterVersion
	}
}
