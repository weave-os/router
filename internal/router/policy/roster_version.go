package policy

func servedRosterVersion(admittedRosterSHA256, sidecarRosterVersion string) string {
	if admittedRosterSHA256 != "" {
		return admittedRosterSHA256
	}
	return sidecarRosterVersion
}
