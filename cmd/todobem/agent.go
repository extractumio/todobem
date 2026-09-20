package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/auth"
	"github.com/extractumio/todobem/internal/fleet"
	"github.com/extractumio/todobem/internal/server"
	"github.com/extractumio/todobem/internal/store"
)

// The command-line side of agent mode (docs/AGENT-MODE.md §4.1, §5.1): `todobem agent pair` on
// an agent host mints a pairing string from the key file; `todobem hub …` on the hub pairs,
// lists, removes, doctors and rotates agents through the agents file — a running hub picks the
// file up by itself, so none of these needs the server.

// runAgentCmd is `todobem agent pair`.
func runAgentCmd(args []string) int {
	if len(args) == 0 || args[0] != "pair" {
		fmt.Fprintln(os.Stderr, "usage: todobem agent pair [-q] [-ttl 365d] [-revoke] [-port 7789] [-key ~/.todobem/agent.key]\n       (the agent itself is `todobem -agent`)")
		return 2
	}
	fs := flag.NewFlagSet("todobem agent pair", flag.ContinueOnError)
	quiet := fs.Bool("q", false, "print the pairing string only")
	ttl := fs.String("ttl", "365d", "how long the hub's bearer lives once the token is redeemed")
	revoke := fs.Bool("revoke", false, "rotate the agent key first: every bearer stops working now")
	port := fs.String("port", fleet.DefaultPort, "the port the agent listens on (for the string)")
	keyPath := fs.String("key", fleet.DefaultAgentKeyPath(), "the agent key file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	d, err := auth.ParseTTL(*ttl)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	certPath, _ := fleet.CertPaths(*keyPath)
	pin, err := fleet.CertPin(certPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no agent certificate at %s: start the agent first (todobem -agent)\n", certPath)
		return 1
	}
	s, err := server.PairingString(*keyPath, pin, *port, *revoke, d, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *quiet {
		fmt.Println(s)
		return 0
	}
	if *revoke {
		fmt.Println("agent key rotated: every bearer is now invalid; pair the hub again")
	}
	fmt.Printf("pair:   %s\n        one use, within %s, against a RUNNING agent (a token minted while it is down is refused)\n        on the hub: todobem hub add '<the string>' [-name web-01] [-addr host:%s]\n", s, auth.TokenWindow, *port)
	return 0
}

// runHub is `todobem hub add|list|remove|doctor|rotate`. Flags come first, as in every other
// subcommand: `hub remove -revoke NAME`, `hub doctor -all`.
func runHub(args []string) int {
	usage := func() int {
		fmt.Fprintln(os.Stderr, `usage: todobem hub add [-name NAME] [-addr HOST:PORT] '<pairing string>'
       todobem hub list
       todobem hub remove [-revoke] NAME
       todobem hub doctor NAME | -all
       todobem hub rotate NAME | -all
flags shared: -agents PATH   the agents file (default ~/.todobem/agents.json or $TODOBEM_AGENTS)`)
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	cmd := args[0]
	fs := flag.NewFlagSet("todobem hub "+cmd, flag.ContinueOnError)
	path := fs.String("agents", fleet.DefaultAgentsPath(), "the agents file")
	name := fs.String("name", "", "the agent's name on the hub (add: default its hostname)")
	addr := fs.String("addr", "", "host:port to dial (add: default the pairing string's)")
	revoke := fs.Bool("revoke", false, "remove: rotate the agent's key first so nobody holds a bearer")
	all := fs.Bool("all", false, "every paired agent")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	target := fs.Arg(0)
	agents, _, err := fleet.LoadAgents(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch cmd {
	case "add":
		if target == "" {
			return usage()
		}
		a, h, err := fleet.Pair(target, *name, *addr)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		verb := "paired"
		for _, x := range agents {
			if x.Name == a.Name {
				verb = "re-paired"
			}
		}
		if err := fleet.Record(*path, a); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("%s %s at %s: todobem %s %s/%s, protocol %d, %d root sessions, digest %d/%d\n        bearer until %s · recorded in %s (a running hub picks it up within %s)\n", verb, a.Name, a.Addr, h.Version, h.OS, h.Arch, h.Protocol, h.Roots, h.Digest.Done, h.Digest.Total, fleet.ExpiresOn(a.Expires), *path, fleet.FileCheck)
		if h.CacheVersion != store.CacheVersion() {
			fmt.Printf("        NOTE: the agent's cache version is %d, this build's %d — the list works, models need the same build on both\n", h.CacheVersion, store.CacheVersion())
		}
		return 0
	case "list":
		if len(agents) == 0 {
			fmt.Printf("no agents paired (%s)\n", *path)
			return 0
		}
		return forEachAgent(agents, func(a fleet.Agent, c *fleet.Client) (string, error) {
			t := time.Now()
			h, err := c.Hello()
			if err != nil {
				return "", err
			}
			compat := ""
			if h.CacheVersion != store.CacheVersion() {
				compat = " · build differs (models unavailable)"
			}
			return fmt.Sprintf("ok %4dms · todobem %s %s/%s · %d sessions · digest %d/%d · bearer until %s%s", time.Since(t).Milliseconds(), h.Version, h.OS, h.Arch, h.Roots, h.Digest.Done, h.Digest.Total, fleet.ExpiresOn(a.Expires), compat), nil
		})
	case "remove":
		if target == "" || *all {
			fmt.Fprintln(os.Stderr, "remove takes one name; delete the agents file to forget every agent")
			return 2
		}
		a, ok := pick(agents, target)
		if !ok {
			fmt.Fprintf(os.Stderr, "no agent named %s in %s\n", target, *path)
			return 1
		}
		if *revoke {
			c := fleet.NewClient(a.Addr, a.Pin, a.Bearer)
			_, err := c.Rotate()
			c.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: revoke: %v (not removed)\n", a.Name, err)
				return 1
			}
		}
		if err := fleet.Forget(*path, a.Name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_ = fleet.RemoveSnapshot(fleet.DefaultSnapshotDir(), a.Name)
		note := ""
		if *revoke {
			note = " and its key rotated (no bearer is valid)"
		}
		fmt.Printf("%s removed%s; its cached sessions go with the next `todobem cache -prune`\n", a.Name, note)
		return 0
	case "doctor", "rotate":
		picked := agents
		if !*all {
			a, ok := pick(agents, target)
			if !ok {
				if target == "" {
					return usage()
				}
				fmt.Fprintf(os.Stderr, "no agent named %s in %s\n", target, *path)
				return 1
			}
			picked = []fleet.Agent{a}
		}
		if cmd == "doctor" {
			return forEachAgent(picked, func(a fleet.Agent, c *fleet.Client) (string, error) {
				rep, err := c.Doctor()
				if err != nil {
					return "", err
				}
				var pretty bytes.Buffer
				if json.Indent(&pretty, rep, "", "  ") == nil {
					rep = pretty.Bytes()
				}
				return "\n" + string(rep), nil
			})
		}
		var mu sync.Mutex
		code := forEachAgent(picked, func(a fleet.Agent, c *fleet.Client) (string, error) {
			resp, err := c.Rotate()
			if err != nil {
				return "", err
			}
			a.Bearer, a.Expires = resp.Bearer, resp.Expires
			mu.Lock()
			defer mu.Unlock()
			if err := fleet.Record(*path, a); err != nil {
				return "", err
			}
			return fmt.Sprintf("key rotation staged, new bearer recorded (until %s); the old key retires on its first use", fleet.ExpiresOn(a.Expires)), nil
		})
		return code
	default:
		return usage()
	}
}

// pick finds an agent by name.
func pick(agents []fleet.Agent, name string) (fleet.Agent, bool) {
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return fleet.Agent{}, false
}

// forEachAgent runs fn against every agent, eight at a time, and prints one line per agent in
// file order; the exit code is 1 when any failed.
func forEachAgent(agents []fleet.Agent, fn func(fleet.Agent, *fleet.Client) (string, error)) int {
	type result struct {
		line string
		err  error
	}
	results := make([]result, len(agents))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, a := range agents {
		wg.Add(1)
		go func(i int, a fleet.Agent) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c := fleet.NewClient(a.Addr, a.Pin, a.Bearer)
			defer c.Close()
			line, err := fn(a, c)
			results[i] = result{line, err}
		}(i, a)
	}
	wg.Wait()
	code := 0
	for i, a := range agents {
		if results[i].err != nil {
			fmt.Printf("%-20s %-24s FAILED: %v\n", a.Name, a.Addr, results[i].err)
			code = 1
			continue
		}
		fmt.Printf("%-20s %-24s %s\n", a.Name, a.Addr, results[i].line)
	}
	return code
}

// portOf is the port part of a listen address (for the pairing string).
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fleet.DefaultPort
	}
	return port
}
