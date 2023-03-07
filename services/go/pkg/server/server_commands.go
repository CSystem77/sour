package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cfoust/sour/pkg/server/protocol/cubecode"
	"github.com/cfoust/sour/pkg/server/protocol/mastermode"
	"github.com/cfoust/sour/pkg/server/protocol/nmc"
	"github.com/cfoust/sour/pkg/server/protocol/role"
)

type ServerCommand struct {
	name        string
	argsFormat  string
	aliases     []string
	description string
	minRole     role.ID
	f           func(s *GameServer, c *Client, args []string)
}

func (cmd *ServerCommand) String() string {
	return fmt.Sprintf("%s %s", cubecode.Green("#"+cmd.name), cmd.argsFormat)
}

func (cmd *ServerCommand) Detailed() string {
	aliases := ""
	if len(cmd.aliases) > 0 {
		aliases = cubecode.Gray(fmt.Sprintf("(alias %s)", strings.Join(cmd.aliases, ", ")))
	}
	return fmt.Sprintf("%s: %s\n%s", cmd.String(), aliases, cmd.description)
}

type ServerCommands struct {
	s       *GameServer
	byName  map[string]*ServerCommand
	byAlias map[string]*ServerCommand
}

func NewCommands(s *GameServer, cmds ...*ServerCommand) *ServerCommands {
	sc := &ServerCommands{
		s:       s,
		byName:  map[string]*ServerCommand{},
		byAlias: map[string]*ServerCommand{},
	}
	for _, cmd := range cmds {
		sc.Register(cmd)
	}
	return sc
}

func (sc *ServerCommands) Register(cmd *ServerCommand) {
	sc.byName[cmd.name] = cmd
	sc.byAlias[cmd.name] = cmd
	for _, alias := range cmd.aliases {
		sc.byAlias[alias] = cmd
	}
}

func (sc *ServerCommands) Unregister(cmd *ServerCommand) {
	for _, alias := range cmd.aliases {
		delete(sc.byAlias, alias)
	}
	delete(sc.byAlias, cmd.name)
	delete(sc.byName, cmd.name)
}

func (sc *ServerCommands) PrintCommands(c *Client) {
	helpLines := []string{}
	for _, cmd := range sc.byName {
		if c.Role >= cmd.minRole {
			helpLines = append(helpLines, cmd.String())
		}
	}
	c.Send(nmc.ServerMessage, "available commands: "+strings.Join(helpLines, ", "))
}

func (sc *ServerCommands) Handle(c *Client, msg string) {
	parts := strings.Split(strings.TrimSpace(msg), " ")
	command, args := parts[0], parts[1:]

	switch command {
	case "help", "commands":
		if len(args) == 0 {
			sc.PrintCommands(c)
			return
		}
		name := args[0]
		if strings.HasPrefix(name, "#") {
			name = name[1:]
		}
		if cmd, ok := sc.byAlias[name]; ok {
			c.Send(nmc.ServerMessage, cmd.Detailed())
		} else {
			c.Send(nmc.ServerMessage, cubecode.Fail("unknown command '"+name+"'"))
		}

	default:
		cmd, ok := sc.byAlias[command]
		if !ok {
			c.Send(nmc.ServerMessage, cubecode.Fail("unknown command '"+command+"'"))
			return
		}

		if c.Role < cmd.minRole {
			return
		}

		cmd.f(sc.s, c, args)
	}
}

var ToggleKeepTeams = &ServerCommand{
	name:        "keepteams",
	argsFormat:  "0|1",
	aliases:     []string{"persist", "persistteams"},
	description: "keeps teams the same across map change",
	minRole:     role.Master,
	f: func(s *GameServer, c *Client, args []string) {
		changed := false
		if len(args) >= 1 {
			val, err := strconv.Atoi(args[0])
			if err != nil || (val != 0 && val != 1) {
				return
			}
			changed = s.KeepTeams != (val == 1)
			s.KeepTeams = val == 1
		}
		if changed {
			if s.KeepTeams {
				s.Clients.Broadcast(nmc.ServerMessage, "teams will be kept")
			} else {
				s.Clients.Broadcast(nmc.ServerMessage, "teams will be shuffled")
			}
		} else {
			if s.KeepTeams {
				c.Send(nmc.ServerMessage, "teams will be kept")
			} else {
				c.Send(nmc.ServerMessage, "teams will be shuffled")
			}
		}
	},
}

var ToggleCompetitiveMode = &ServerCommand{
	name:        "competitive",
	argsFormat:  "0|1",
	aliases:     []string{"comp"},
	description: "in competitive mode, the server waits for all clients to load the map and auto-pauses when a player leaves the game",
	minRole:     role.Master,
	f: func(s *GameServer, c *Client, args []string) {
		changed := false
		if len(args) >= 1 {
			val, err := strconv.Atoi(args[0])
			if err != nil || (val != 0 && val != 1) {
				return
			}
			changed = s.CompetitiveMode != (val == 1)
			switch val {
			case 1:
				// starts at next map
				s.CompetitiveMode = true
				// but lock server now
				s.SetMasterMode(c, mastermode.Locked)
			default:
				s.CompetitiveMode = false
			}
		}
		if changed {
			if s.CompetitiveMode {
				s.Clients.Broadcast(nmc.ServerMessage, "competitive mode will be enabled with next game")
			} else {
				s.Clients.Broadcast(nmc.ServerMessage, "competitive mode will be disabled with next game")
			}
		} else {
			if s.CompetitiveMode {
				c.Send(nmc.ServerMessage, "competitive mode is on")
			} else {
				c.Send(nmc.ServerMessage, "competitive mode is off")
			}
		}
	},
}

var ToggleReportStats = &ServerCommand{
	name:        "reportstats",
	argsFormat:  "0|1",
	aliases:     []string{"repstats"},
	description: "when enabled, end-game stats of players will be reported at intermission",
	minRole:     role.Admin,
	f: func(s *GameServer, c *Client, args []string) {
		changed := false
		if len(args) >= 1 {
			val, err := strconv.Atoi(args[0])
			if err != nil || (val != 0 && val != 1) {
				return
			}
			changed = s.ReportStats != (val == 1)
			s.ReportStats = val == 1
		}
		if changed {
			if s.ReportStats {
				s.Clients.Broadcast(nmc.ServerMessage, "stats will be reported at intermission")
			} else {
				s.Clients.Broadcast(nmc.ServerMessage, "stats will not be reported")
			}
		} else {
			if s.ReportStats {
				c.Send(nmc.ServerMessage, "stats reporting is on")
			} else {
				c.Send(nmc.ServerMessage, "stats reporting is off")
			}
		}
	},
}

var SetTimeLeft = &ServerCommand{
	name:        "settime",
	argsFormat:  "[Xm][Ys]",
	aliases:     []string{"time", "settimeleft", "settimeremaining", "timeleft", "timeremaining"},
	description: "sets the time remaining to play to X minutes and Y seconds",
	minRole:     role.Admin,
	f: func(s *GameServer, c *Client, args []string) {
		if len(args) < 1 {
			return
		}

		d, err := time.ParseDuration(args[0])
		if err != nil {
			c.Send(nmc.ServerMessage, cubecode.Error("could not parse duration: "+err.Error()))
			return
		}

		if d == 0 {
			d = 1 * time.Second // 0 forces intermission without updating the client's game timer
			s.Broadcast(nmc.ServerMessage, fmt.Sprintf("%s forced intermission", s.Clients.UniqueName(c)))
		} else {
			s.Broadcast(nmc.ServerMessage, fmt.Sprintf("%s set the time remaining to %s", s.Clients.UniqueName(c), d))
		}

		s.Clock.SetTimeLeft(d)
	},
}
