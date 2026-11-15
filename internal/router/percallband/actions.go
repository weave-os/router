package percallband

type Action string

const (
	Reason      Action = "reason"
	Explore     Action = "explore"
	Execute     Action = "execute"
	Edit        Action = "edit"
	Orchestrate Action = "orchestrate"
	Integrate   Action = "integrate"
)

var ActionTypes = [...]Action{Reason, Explore, Execute, Edit, Orchestrate, Integrate}

var actionIndex = func() map[Action]int {
	m := make(map[Action]int, len(ActionTypes))
	for i, a := range ActionTypes {
		m[a] = i
	}
	return m
}()

const noneIndex = len(ActionTypes)

type Band string

const (
	LargeBand Band = "large"
	SmallBand Band = "small"
)

var (
	exploreTools     = set("Read", "Grep", "Glob", "LS", "ToolSearch", "NotebookRead", "WebFetch", "WebSearch")
	editTools        = set("Edit", "Write", "MultiEdit", "NotebookEdit", "ApplyPatch")
	executeTools     = set("Bash", "PowerShell")
	orchestrateTools = set("Task", "Agent", "Skill", "TaskCreate", "TaskUpdate", "TaskList")
)

func set(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

func ActionOfTool(tool string) Action {
	switch {
	case has(exploreTools, tool):
		return Explore
	case has(editTools, tool):
		return Edit
	case has(executeTools, tool):
		return Execute
	case has(orchestrateTools, tool):
		return Orchestrate
	default:
		return Integrate
	}
}

func has(m map[string]struct{}, k string) bool {
	_, ok := m[k]
	return ok
}

func DeriveAction(hasTool bool, firstTool string) Action {
	if !hasTool || firstTool == "" {
		return Reason
	}
	return ActionOfTool(firstTool)
}
