package version

const shortLen = 7

var (
	Commit    = "unknown"
	BuildTime = "unknown"
	PR        = "unknown"
)

func ShortCommit() string {
	if len(Commit) <= shortLen {
		return Commit
	}
	return Commit[:shortLen]
}

func Display() string {
	if PR != "" && PR != "unknown" {
		return "#" + PR + " (" + ShortCommit() + ")"
	}
	return ShortCommit()
}
