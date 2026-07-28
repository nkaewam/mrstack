package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CommandName is the stable name emitted by the agent API.
type CommandName string

const (
	CommandDoctor          CommandName = "doctor"
	CommandCheck           CommandName = "check"
	CommandRestackStart    CommandName = "restack.start"
	CommandRestackPlan     CommandName = "restack.plan"
	CommandRestackContinue CommandName = "restack.continue"
	CommandRestackAbort    CommandName = "restack.abort"
	CommandRestackRecover  CommandName = "restack.recover"
	CommandRestackAbandon  CommandName = "restack.abandon"
	CommandCILogs          CommandName = "ci.logs"
	CommandHistoryShow     CommandName = "history.show"
	CommandHistoryAlias    CommandName = "history.alias"
	CommandHistoryPrune    CommandName = "history.prune"
	CommandUnknown         CommandName = "unknown"
)

type Globals struct {
	JSON       bool
	NoInput    bool
	Yes        bool
	Remote     string
	GitLabMode string
	Debug      bool
}

type Selector struct {
	Value   string
	StackID string
}

type Invocation struct {
	Name                CommandName
	Globals             Globals
	Selector            Selector
	SnapshotID          string
	PlanID              string
	SessionID           string
	PipelineID          string
	JobIDs              []string
	MaxBytes            int64
	Limit               int
	Cursor              string
	Alias               string
	ClearAlias          bool
	Before              string
	Boundaries          []LayerBoundary
	AllowSignatureLoss  bool
	DropCurrent         bool
	KeepEmpty           bool
	AcceptCurrentRemote bool
	Help                bool
	ShowVersion         bool
}

type LayerBoundary struct {
	MR  string
	SHA string
}

func (i Invocation) Machine() bool { return i.Globals.JSON && i.Globals.NoInput }

const HelpText = `Usage:
  mrstack [global options] <command> [options]

Commands:
  doctor
  check [<MR-or-branch> | --stack <id>]
  restack [<MR-or-branch>] --snapshot <id>
  restack plan [<MR-or-branch>] --snapshot <id> --layer-boundary <mr>=<sha>
  restack --plan <id>
  restack continue|abort|recover|abandon ...
  ci logs --pipeline <id> --job <id>...
  history [<MR-or-branch> | --stack <id>]
  history alias|prune ...

Global options:
  --json  --no-input  --yes  --remote <name>
  --gitlab-mode <auto|legacy|native>
  --debug  print each git/glab subprocess argv and stderr
  -h, --help  --version
`

// Parse accepts global options at any argv position and validates all v1
// command forms without consulting the environment.
func Parse(args []string) (Invocation, error) {
	clean, globals, err := extractGlobals(args)
	inv := Invocation{Globals: globals}
	if err != nil {
		return inv, err
	}
	if globals.JSON != globals.NoInput {
		return inv, Invalid("invalid_arguments", "--json and --no-input must be used together")
	}
	if len(clean) == 0 {
		return inv, Invalid("invalid_arguments", "a command is required")
	}
	helpRequested := clean[0] == "help"
	for _, arg := range clean {
		if arg == "-h" || arg == "--help" {
			helpRequested = true
		}
	}
	if helpRequested {
		if inv.Machine() {
			return inv, Invalid("invalid_arguments", "help is human-readable only")
		}
		inv.Name = commandNameFromArgs(clean)
		inv.Help = true
		return inv, nil
	}
	if len(clean) == 1 && clean[0] == "--version" {
		if inv.Machine() {
			return inv, Invalid("invalid_arguments", "version is human-readable only")
		}
		inv.ShowVersion = true
		return inv, nil
	}

	switch clean[0] {
	case "doctor":
		inv.Name = CommandDoctor
		err = noArgs(clean[1:], "doctor")
	case "check":
		inv.Name = CommandCheck
		err = parseSelected(clean[1:], &inv, nil)
	case "restack":
		err = parseRestack(clean[1:], &inv)
	case "ci":
		err = parseCI(clean[1:], &inv)
	case "history":
		err = parseHistory(clean[1:], &inv)
	default:
		inv.Name = CommandUnknown
		err = Invalid("unknown_command", fmt.Sprintf("unknown command %q", clean[0]))
	}
	if err != nil {
		return inv, err
	}
	if inv.Machine() && mutationNeedsYes(inv.Name) && !inv.Globals.Yes {
		return inv, Invalid("invalid_arguments", "--yes is required for this command in machine mode")
	}
	return inv, nil
}

func extractGlobals(args []string) ([]string, Globals, error) {
	var g Globals
	var clean []string
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, value, hasEquals := splitOption(arg)
		switch key {
		case "--json", "--no-input", "--yes", "--debug":
			if hasEquals {
				return clean, g, Invalid("invalid_arguments", key+" does not take a value")
			}
			if seen[key] {
				return clean, g, Invalid("invalid_arguments", key+" was supplied more than once")
			}
			seen[key] = true
			switch key {
			case "--json":
				g.JSON = true
			case "--no-input":
				g.NoInput = true
			case "--yes":
				g.Yes = true
			case "--debug":
				g.Debug = true
			}
		case "--remote", "--gitlab-mode":
			if seen[key] {
				return clean, g, Invalid("invalid_arguments", key+" was supplied more than once")
			}
			seen[key] = true
			if !hasEquals {
				i++
				if i >= len(args) || strings.HasPrefix(args[i], "--") {
					return clean, g, Invalid("invalid_arguments", key+" requires a value")
				}
				value = args[i]
			}
			if value == "" {
				return clean, g, Invalid("invalid_arguments", key+" requires a non-empty value")
			}
			if key == "--remote" {
				g.Remote = value
			} else {
				if value != "auto" && value != "legacy" && value != "native" {
					return clean, g, Invalid("invalid_arguments", "--gitlab-mode must be auto, legacy, or native")
				}
				g.GitLabMode = value
			}
		default:
			clean = append(clean, arg)
		}
	}
	if g.GitLabMode == "" {
		g.GitLabMode = "auto"
	}
	return clean, g, nil
}

func parseRestack(args []string, inv *Invocation) error {
	if len(args) > 0 {
		switch args[0] {
		case "plan":
			inv.Name = CommandRestackPlan
			return parseRestackPlan(args[1:], inv)
		case "continue":
			inv.Name = CommandRestackContinue
			return parseSessionCommand(args[1:], inv, true)
		case "abort":
			inv.Name = CommandRestackAbort
			return parseSessionCommand(args[1:], inv, false)
		case "recover":
			inv.Name = CommandRestackRecover
			return parseSessionCommand(args[1:], inv, false)
		case "abandon":
			inv.Name = CommandRestackAbandon
			return parseAbandon(args[1:], inv)
		}
	}
	inv.Name = CommandRestackStart
	var positionals []string
	for i := 0; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		switch key {
		case "--snapshot", "--plan":
			if value == "" && !eq {
				var err error
				value, i, err = takeValue(args, i, key)
				if err != nil {
					return err
				}
			}
			if value == "" {
				return Invalid("invalid_arguments", key+" requires a value")
			}
			if key == "--snapshot" {
				if inv.SnapshotID != "" {
					return duplicate(key)
				}
				inv.SnapshotID = value
			} else {
				if inv.PlanID != "" {
					return duplicate(key)
				}
				inv.PlanID = value
			}
		case "--allow-signature-loss":
			if eq || inv.AllowSignatureLoss {
				return flagError(key, eq)
			}
			inv.AllowSignatureLoss = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return unknownOption(args[i])
			}
			positionals = append(positionals, args[i])
		}
	}
	if len(positionals) > 1 {
		return Invalid("invalid_arguments", "restack accepts at most one selector")
	}
	if inv.PlanID != "" {
		if inv.SnapshotID != "" || len(positionals) != 0 {
			return Invalid("invalid_arguments", "--plan cannot be combined with --snapshot or a selector")
		}
	} else {
		if inv.SnapshotID == "" {
			return Invalid("invalid_arguments", "--snapshot is required")
		}
		if len(positionals) == 1 {
			inv.Selector.Value = positionals[0]
		}
	}
	return nil
}

func parseRestackPlan(args []string, inv *Invocation) error {
	var positionals []string
	for i := 0; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		switch key {
		case "--snapshot", "--layer-boundary":
			if value == "" && !eq {
				var err error
				value, i, err = takeValue(args, i, key)
				if err != nil {
					return err
				}
			}
			if value == "" {
				return Invalid("invalid_arguments", key+" requires a value")
			}
			if key == "--snapshot" {
				if inv.SnapshotID != "" {
					return duplicate(key)
				}
				inv.SnapshotID = value
			} else {
				parts := strings.Split(value, "=")
				if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
					return Invalid("invalid_arguments", "--layer-boundary must be <mr>=<sha>")
				}
				if !positiveDecimal(parts[0]) || !fullObjectID(parts[1]) {
					return Invalid("invalid_arguments", "--layer-boundary requires a positive MR IID and full 40- or 64-hex SHA")
				}
				inv.Boundaries = append(inv.Boundaries, LayerBoundary{MR: parts[0], SHA: parts[1]})
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return unknownOption(args[i])
			}
			positionals = append(positionals, args[i])
		}
	}
	if inv.SnapshotID == "" {
		return Invalid("invalid_arguments", "--snapshot is required")
	}
	if len(inv.Boundaries) == 0 {
		return Invalid("invalid_arguments", "at least one --layer-boundary is required")
	}
	if len(positionals) > 1 {
		return Invalid("invalid_arguments", "restack plan accepts at most one selector")
	}
	if len(positionals) == 1 {
		inv.Selector.Value = positionals[0]
	}
	return nil
}

func parseSessionCommand(args []string, inv *Invocation, choices bool) error {
	for i := 0; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		switch key {
		case "--session":
			if value == "" && !eq {
				var err error
				value, i, err = takeValue(args, i, key)
				if err != nil {
					return err
				}
			}
			if value == "" {
				return Invalid("invalid_arguments", "--session requires a value")
			}
			if inv.SessionID != "" {
				return duplicate(key)
			}
			inv.SessionID = value
		case "--drop-current", "--keep-empty":
			if !choices {
				return unknownOption(key)
			}
			if eq {
				return flagError(key, true)
			}
			if key == "--drop-current" {
				if inv.DropCurrent {
					return duplicate(key)
				}
				inv.DropCurrent = true
			} else {
				if inv.KeepEmpty {
					return duplicate(key)
				}
				inv.KeepEmpty = true
			}
		default:
			return unknownOption(args[i])
		}
	}
	if inv.SessionID == "" {
		return Invalid("invalid_arguments", "--session is required")
	}
	if inv.DropCurrent && inv.KeepEmpty {
		return Invalid("invalid_arguments", "--drop-current and --keep-empty are mutually exclusive")
	}
	return nil
}

func parseAbandon(args []string, inv *Invocation) error {
	for i := 0; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		switch key {
		case "--session":
			if value == "" && !eq {
				var err error
				value, i, err = takeValue(args, i, key)
				if err != nil {
					return err
				}
			}
			if value == "" {
				return Invalid("invalid_arguments", "--session requires a value")
			}
			if inv.SessionID != "" {
				return duplicate(key)
			}
			inv.SessionID = value
		case "--accept-current-remote":
			if eq || inv.AcceptCurrentRemote {
				return flagError(key, eq)
			}
			inv.AcceptCurrentRemote = true
		default:
			return unknownOption(args[i])
		}
	}
	if inv.SessionID == "" || !inv.AcceptCurrentRemote {
		return Invalid("invalid_arguments", "--session and --accept-current-remote are required")
	}
	if inv.Globals.NoInput {
		return Invalid("invalid_arguments", "restack abandon is unavailable with --no-input")
	}
	return nil
}

func parseCI(args []string, inv *Invocation) error {
	if len(args) == 0 || args[0] != "logs" {
		inv.Name = CommandUnknown
		return Invalid("unknown_command", "expected \"ci logs\"")
	}
	inv.Name = CommandCILogs
	inv.MaxBytes = 524288
	maxBytesSeen := false
	for i := 1; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		switch key {
		case "--pipeline", "--job", "--max-bytes":
			if value == "" && !eq {
				var err error
				value, i, err = takeValue(args, i, key)
				if err != nil {
					return err
				}
			}
			if value == "" {
				return Invalid("invalid_arguments", key+" requires a value")
			}
			switch key {
			case "--pipeline":
				if inv.PipelineID != "" {
					return duplicate(key)
				}
				if !decimal(value) {
					return Invalid("invalid_arguments", "--pipeline must be a decimal ID")
				}
				inv.PipelineID = value
			case "--job":
				if !decimal(value) {
					return Invalid("invalid_arguments", "--job must be a decimal ID")
				}
				inv.JobIDs = append(inv.JobIDs, value)
			case "--max-bytes":
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil || n < 1 || n > 4194304 {
					return Invalid("invalid_arguments", "--max-bytes must be between 1 and 4194304")
				}
				if maxBytesSeen {
					return duplicate(key)
				}
				maxBytesSeen = true
				inv.MaxBytes = n
			}
		default:
			return unknownOption(args[i])
		}
	}
	if inv.PipelineID == "" || len(inv.JobIDs) == 0 {
		return Invalid("invalid_arguments", "--pipeline and at least one --job are required")
	}
	if len(inv.JobIDs) > 20 {
		return Invalid("invalid_arguments", "at most 20 --job values are accepted")
	}
	return nil
}

func parseHistory(args []string, inv *Invocation) error {
	if len(args) > 0 {
		switch args[0] {
		case "alias":
			inv.Name = CommandHistoryAlias
			return parseHistoryAlias(args[1:], inv)
		case "prune":
			inv.Name = CommandHistoryPrune
			return parseHistoryPrune(args[1:], inv)
		}
	}
	inv.Name = CommandHistoryShow
	inv.Limit = 50
	limitSeen := false
	return parseSelected(args, inv, map[string]func(string) error{
		"--limit": func(value string) error {
			if limitSeen {
				return duplicate("--limit")
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 200 {
				return Invalid("invalid_arguments", "--limit must be between 1 and 200")
			}
			limitSeen = true
			inv.Limit = n
			return nil
		},
		"--cursor": func(value string) error {
			if inv.Cursor != "" {
				return duplicate("--cursor")
			}
			inv.Cursor = value
			return nil
		},
	})
}

func parseSelected(args []string, inv *Invocation, extra map[string]func(string) error) error {
	var positionals []string
	for i := 0; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		if key == "--stack" || extra[key] != nil {
			if value == "" && !eq {
				var err error
				value, i, err = takeValue(args, i, key)
				if err != nil {
					return err
				}
			}
			if value == "" {
				return Invalid("invalid_arguments", key+" requires a value")
			}
			if key == "--stack" {
				if inv.Selector.StackID != "" {
					return duplicate(key)
				}
				inv.Selector.StackID = value
			} else if err := extra[key](value); err != nil {
				return err
			}
		} else if strings.HasPrefix(args[i], "-") {
			return unknownOption(args[i])
		} else {
			positionals = append(positionals, args[i])
		}
	}
	if len(positionals) > 1 {
		return Invalid("invalid_arguments", "at most one selector is accepted")
	}
	if len(positionals) == 1 {
		inv.Selector.Value = positionals[0]
	}
	if inv.Selector.Value != "" && inv.Selector.StackID != "" {
		return Invalid("invalid_arguments", "a selector and --stack are mutually exclusive")
	}
	return nil
}

func parseHistoryAlias(args []string, inv *Invocation) error {
	var aliases []string
	for i := 0; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		switch key {
		case "--stack":
			if value == "" && !eq {
				var err error
				value, i, err = takeValue(args, i, key)
				if err != nil {
					return err
				}
			}
			if value == "" {
				return Invalid("invalid_arguments", "--stack requires a value")
			}
			if inv.Selector.StackID != "" {
				return duplicate(key)
			}
			inv.Selector.StackID = value
		case "--clear":
			if eq || inv.ClearAlias {
				return flagError(key, eq)
			}
			inv.ClearAlias = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return unknownOption(args[i])
			}
			aliases = append(aliases, args[i])
		}
	}
	if inv.Selector.StackID == "" {
		return Invalid("invalid_arguments", "--stack is required")
	}
	if len(aliases) > 1 || (len(aliases) == 1 && inv.ClearAlias) || (len(aliases) == 0 && !inv.ClearAlias) {
		return Invalid("invalid_arguments", "exactly one alias or --clear is required")
	}
	if len(aliases) == 1 {
		inv.Alias = aliases[0]
	}
	return nil
}

func parseHistoryPrune(args []string, inv *Invocation) error {
	for i := 0; i < len(args); i++ {
		key, value, eq := splitOption(args[i])
		if key != "--before" && key != "--stack" {
			return unknownOption(args[i])
		}
		if value == "" && !eq {
			var err error
			value, i, err = takeValue(args, i, key)
			if err != nil {
				return err
			}
		}
		if value == "" {
			return Invalid("invalid_arguments", key+" requires a value")
		}
		if key == "--before" {
			if inv.Before != "" {
				return duplicate(key)
			}
			inv.Before = value
		} else {
			if inv.Selector.StackID != "" {
				return duplicate(key)
			}
			inv.Selector.StackID = value
		}
	}
	if inv.Before == "" {
		return Invalid("invalid_arguments", "--before is required")
	}
	if !timestampOrDuration(inv.Before) {
		return Invalid("invalid_arguments", "--before must be an RFC 3339 timestamp or duration")
	}
	return nil
}

func mutationNeedsYes(name CommandName) bool {
	switch name {
	case CommandRestackStart, CommandRestackContinue, CommandRestackAbort,
		CommandHistoryPrune:
		return true
	default:
		return false
	}
}

func commandNameFromArgs(args []string) CommandName {
	clean, _, _ := extractGlobals(args)
	if len(clean) == 0 {
		return CommandUnknown
	}
	switch clean[0] {
	case "doctor":
		return CommandDoctor
	case "check":
		return CommandCheck
	case "restack":
		if len(clean) > 1 {
			switch clean[1] {
			case "plan":
				return CommandRestackPlan
			case "continue":
				return CommandRestackContinue
			case "abort":
				return CommandRestackAbort
			case "recover":
				return CommandRestackRecover
			case "abandon":
				return CommandRestackAbandon
			}
		}
		return CommandRestackStart
	case "ci":
		if len(clean) > 1 && clean[1] == "logs" {
			return CommandCILogs
		}
	case "history":
		if len(clean) > 1 {
			if clean[1] == "alias" {
				return CommandHistoryAlias
			}
			if clean[1] == "prune" {
				return CommandHistoryPrune
			}
		}
		return CommandHistoryShow
	}
	return CommandUnknown
}

func wantsJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

func splitOption(arg string) (string, string, bool) {
	if strings.HasPrefix(arg, "--") {
		if at := strings.IndexByte(arg, '='); at >= 0 {
			return arg[:at], arg[at+1:], true
		}
	}
	return arg, "", false
}

func takeValue(args []string, i int, option string) (string, int, error) {
	i++
	if i >= len(args) || strings.HasPrefix(args[i], "--") {
		return "", i, Invalid("invalid_arguments", option+" requires a value")
	}
	return args[i], i, nil
}

func decimal(s string) bool {
	if s == "0" {
		return true
	}
	if s == "" || s[0] == '0' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func positiveDecimal(s string) bool {
	return s != "0" && decimal(s)
}

func fullObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

var durationPattern = regexp.MustCompile(`^([1-9][0-9]*(ns|us|µs|ms|s|m|h|d|w))+$`)

func timestampOrDuration(s string) bool {
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	return durationPattern.MatchString(s)
}

func noArgs(args []string, command string) error {
	if len(args) != 0 {
		return Invalid("invalid_arguments", command+" does not accept arguments")
	}
	return nil
}

func duplicate(option string) error {
	return Invalid("invalid_arguments", option+" was supplied more than once")
}

func unknownOption(option string) error {
	return Invalid("invalid_arguments", fmt.Sprintf("unknown option or argument %q", option))
}

func flagError(option string, hasValue bool) error {
	if hasValue {
		return Invalid("invalid_arguments", option+" does not take a value")
	}
	return duplicate(option)
}
