package policy

func servedRosterVersion(admittedPolicyDigest, sidecarRosterVersion string) string {
	if admittedPolicyDigest != "" {
		return admittedPolicyDigest
	}
	return sidecarRosterVersion
}
