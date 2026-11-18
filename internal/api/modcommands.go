package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/rcon"
)

// The game's own tree has none of the commands a server's mods and plugins
// add. The game's help command lists every command the server has, in
// Brigadier's short form of usage:
//
//	/bingo [card|difficulty|reroll|lobby|goal|mode|options|timelimit]
//	/bingo [difficulty] (<S>|easy|medium|hard|extreme|impossible)
//	/tp -> teleport
//
// [a|b] are subcommands the one before runs without, (a|b) one it needs,
// <name> an argument, "-> name" another command it is a name for, and
// "..." the start of a command again. So, once the console asks for a
// running server's commands, the panel asks the game for its help over
// RCON, takes the commands its version's tree does not have, asks for the
// help of each subcommand whose own subcommands it does not know yet, and
// adds what it read to the tree the console completes from. Only names are
// known that way: an argument's kind is not, so it is completed by its name
// alone. What was read is kept until the game is next started.
//
// Paper's help is Bukkit's, pages of topics; there the game's own is
// minecraft:help, and Bukkit's topics are read for names only where that
// says nothing.

// modsWait is how long an ask for a server's commands waits for its help
// to be read before it is answered to ask again.
var modsWait = 4 * time.Second

const (
	// modsTimeout is how long reading a server's help may take in all.
	modsTimeout = 20 * time.Second
	// modsRetry is how long after the help could not be read it is asked
	// for again.
	modsRetry = time.Minute
	// modsCalls is how many times the help is asked for, at most, for one
	// server, and modsDepth how many words deep.
	modsCalls = 256
	modsDepth = 6
	// modsBytes is how much help is read, at most, for one server.
	modsBytes = 4 << 20
	// bukkitPages is how many of Bukkit's pages of help are read, at most.
	bukkitPages = 40
)

// usageNode is a command, or a part of one, read from the game's help: as
// the game's tree has it, with a literal word or a named argument of no
// known kind.
type usageNode struct {
	Type       string
	Executable bool
	Redirect   []string
	children   map[string]*usageNode
	order      []string
	// open: named only among others, or last in brackets, so what comes
	// after it is not known yet.
	open bool
	// toRoot: a command starts again after it ("...").
	toRoot bool
	// asked: its help was asked for.
	asked bool
}

func (n *usageNode) child(name string) (*usageNode, bool) {
	typ, key := "literal", name
	if strings.HasPrefix(name, "<") && strings.HasSuffix(name, ">") {
		typ, key = "argument", name[1:len(name)-1]
	}
	if c := n.children[key]; c != nil {
		return c, false
	}
	if n.children == nil {
		n.children = map[string]*usageNode{}
	}
	c := &usageNode{Type: typ}
	n.children[key] = c
	n.order = append(n.order, key)
	return c, true
}

// MarshalJSON writes a node as a node of the game's commands.json, its
// children in the order the game listed them. An argument has no parser:
// its kind is not known.
func (n *usageNode) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"type":`)
	t, _ := json.Marshal(n.Type)
	b.Write(t)
	if len(n.order) > 0 {
		b.WriteString(`,"children":{`)
		for i, name := range n.order {
			if i > 0 {
				b.WriteByte(',')
			}
			k, _ := json.Marshal(name)
			b.Write(k)
			b.WriteByte(':')
			c, err := n.children[name].MarshalJSON()
			if err != nil {
				return nil, err
			}
			b.Write(c)
		}
		b.WriteByte('}')
	}
	if n.Executable {
		b.WriteString(`,"executable":true`)
	}
	if len(n.Redirect) > 0 {
		r, _ := json.Marshal(n.Redirect)
		b.WriteString(`,"redirect":`)
		b.Write(r)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// usageLine is one line of help, after the command it is the help of.
type usageLine struct {
	// chain is one node after another, each the only one after the one
	// before; optional, the first was shown in brackets: the command
	// before it runs without it.
	chain    []string
	optional bool
	// choices are what can come after the last, when there are several,
	// or one shown in brackets; choicesOptional, the last runs without.
	choices         []string
	choicesOptional bool
	toRoot          bool
	redirect        string
}

// A command's words and its arguments' names: no brackets, and no quotes
// or commas, which are in the game's messages but in none of its commands.
var (
	usageLiteral  = regexp.MustCompile(`^[\w.:+\-/?!*~=@#$%&^]+$`)
	usageArgument = regexp.MustCompile(`^<[\w.:+\-/?!*~=@#$%&^]+>$`)
)

func usageName(s string) bool { return usageLiteral.MatchString(s) || usageArgument.MatchString(s) }

// parseUsage reads one line of the game's help, after the command it is
// the help of. ok is false for what is not one.
func parseUsage(s string) (u usageLine, ok bool) {
	toks := strings.Fields(s)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		last := i == len(toks)-1
		switch {
		case t == "...":
			if !last || len(u.chain) == 0 {
				return u, false
			}
			u.toRoot = true
		case t == "->":
			if i != len(toks)-2 || len(u.chain) == 0 || !usageName(toks[i+1]) {
				return u, false
			}
			u.redirect = toks[i+1]
			i++
		case len(t) >= 2 && (t[0] == '[' && t[len(t)-1] == ']' || t[0] == '(' && t[len(t)-1] == ')'):
			alts := strings.Split(t[1:len(t)-1], "|")
			for _, a := range alts {
				if !usageName(a) {
					return u, false
				}
			}
			bracket := t[0] == '['
			switch {
			case i == 0 && bracket && len(alts) == 1:
				u.chain, u.optional = append(u.chain, alts[0]), true
			case i > 0 && last && (len(alts) > 1 || bracket):
				u.choices, u.choicesOptional = alts, bracket
			default:
				return u, false
			}
		default:
			if !usageName(t) {
				return u, false
			}
			u.chain = append(u.chain, t)
		}
	}
	return u, len(u.chain) > 0
}

// add puts what a line of help says into the tree under n, the command it
// is the help of.
func (n *usageNode) add(u usageLine) {
	at := n
	for i, name := range u.chain {
		if i == 0 && u.optional {
			at.Executable = true
		}
		c, _ := at.child(name)
		// The line says all that comes after it.
		c.open = false
		at = c
	}
	switch {
	case u.toRoot:
		at.toRoot = true
	case u.redirect != "":
		at.Redirect = []string{strings.Trim(u.redirect, "<>")}
	case u.choices != nil:
		if u.choicesOptional {
			at.Executable = true
		}
		for _, name := range u.choices {
			if c, made := at.child(name); made {
				c.open = true
			}
		}
	default:
		// Nothing comes after it: it runs as it is.
		at.Executable = true
	}
}

// helpLines splits what the game answered to help into its lines, each
// without the command it is the help of. Over RCON they come with nothing
// between them; at a console, one to a line.
func helpLines(out, path string) []string {
	var lines []string
	for _, l := range strings.Split(plain(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if path != "" {
			parts := strings.Split(l+" ", "/"+path+" ")
			for _, p := range parts[1:] {
				if p = strings.TrimSpace(p); p != "" {
					lines = append(lines, p)
				}
			}
			continue
		}
		// The root's help: each command after a slash, outside brackets. A
		// slash right after one is the command's own, as in WorldEdit's
		// "//wand".
		var cur strings.Builder
		depth, started := 0, false
		for _, r := range l {
			if r == '/' && depth == 0 && (!started || cur.Len() > 0) {
				if started {
					lines = append(lines, strings.TrimSpace(cur.String()))
				}
				cur.Reset()
				started = true
				continue
			}
			switch r {
			case '[', '(', '<':
				depth++
			case ']', ')', '>':
				if depth > 0 {
					depth--
				}
			}
			if started {
				cur.WriteRune(r)
			}
		}
		if started && strings.TrimSpace(cur.String()) != "" {
			lines = append(lines, strings.TrimSpace(cur.String()))
		}
	}
	return lines
}

var (
	bukkitIndex   = regexp.MustCompile(`Help: Index \((\d+)/(\d+)\)`)
	bukkitCommand = regexp.MustCompile(`(?m)^/(\S+): `)
)

// bukkitHelp says whether an answer to help is Bukkit's: pages of topics,
// each command with what it does.
func bukkitHelp(out string) bool {
	out = plain(out)
	return bukkitIndex.MatchString(out) || bukkitCommand.MatchString(out)
}

// helpCommand says whether a command is one the panel asks the game for
// its help with: help, minecraft:help, alone or with what to help with.
// Nothing else is ever run to read the commands.
func helpCommand(command string) bool {
	name, _, _ := strings.Cut(command, " ")
	return (name == "help" || name == "minecraft:help") && !strings.ContainsAny(command, "\r\n")
}

// readHelp reads the commands a server has that its game version's tree
// does not, from the game's help. run runs one command on the game. What
// was read before an error is returned with it.
func readHelp(ctx context.Context, run func(string) (string, error), vanilla map[string]bool) (*usageNode, error) {
	root := &usageNode{Type: "root"}
	calls, read := 0, 0
	ask := func(command string) (string, error) {
		if !helpCommand(command) {
			return "", fmt.Errorf("not a help command: %q", command)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if read > modsBytes {
			return "", errors.New("the server's help is too long")
		}
		calls++
		out, err := run(command)
		read += len(out)
		return out, err
	}
	help := "help"
	out, err := ask(help)
	if err != nil {
		return root, err
	}
	var bukkit []string
	if bukkitHelp(out) {
		// Paper's help is Bukkit's; the game's own goes by its namespace.
		bukkit = append(bukkit, out)
		help = "minecraft:help"
		if out, err = ask(help); err != nil {
			return root, err
		}
	}
	lines := map[string]usageLine{}
	var order []string
	for _, l := range helpLines(out, "") {
		u, ok := parseUsage(l)
		if !ok || vanilla[u.chain[0]] {
			continue
		}
		if _, seen := lines[u.chain[0]]; !seen {
			order = append(order, u.chain[0])
		}
		lines[u.chain[0]] = u
	}
	for _, name := range order {
		// A namespaced name of a command there is also without it is left
		// out, as minecraft:give is for give.
		if ns, bare, ok := strings.Cut(name, ":"); ok && ns != "" && (vanilla[bare] || lines[bare].chain != nil) {
			continue
		}
		u := lines[name]
		u.optional = false
		root.add(u)
	}
	if len(root.order) == 0 && bukkit != nil {
		return root, readBukkitHelp(root, bukkit[0], ask, vanilla)
	}

	// Then the help of each command whose subcommands are only named, as
	// far down as words go (an argument's value cannot be asked about).
	type at struct {
		node *usageNode
		path []string
	}
	var queue []at
	var collect func(n *usageNode, path []string)
	collect = func(n *usageNode, path []string) {
		for _, name := range n.order {
			c := n.children[name]
			if c.Type != "literal" || len(path) >= modsDepth {
				continue
			}
			p := append(append([]string{}, path...), name)
			if !c.asked && c.hasOpenLiteral() {
				queue = append(queue, at{c, p})
			}
			collect(c, p)
		}
	}
	collect(root, nil)
	for len(queue) > 0 && calls < modsCalls {
		next := queue[0]
		queue = queue[1:]
		if next.node.asked {
			continue
		}
		next.node.asked = true
		path := strings.Join(next.path, " ")
		out, err := ask(help + " " + path)
		if err != nil {
			return root.finish(), err
		}
		for _, l := range helpLines(out, path) {
			if u, ok := parseUsage(l); ok {
				next.node.add(u)
			}
		}
		collect(next.node, next.path)
	}
	return root.finish(), nil
}

func (n *usageNode) hasOpenLiteral() bool {
	for _, c := range n.children {
		if c.open && c.Type == "literal" {
			return true
		}
	}
	return false
}

// finish makes each node with nothing known after it one that runs as it
// is, but for those that start a command again.
func (n *usageNode) finish() *usageNode {
	for _, c := range n.children {
		c.finish()
	}
	if n.Type != "root" && len(n.children) == 0 && !n.toRoot && len(n.Redirect) == 0 {
		n.Executable = true
	}
	return n
}

// readBukkitHelp reads command names from Bukkit's pages of help, where
// the game's own help says nothing: names only, each a command that runs.
func readBukkitHelp(root *usageNode, first string, ask func(string) (string, error), vanilla map[string]bool) error {
	pages := 1
	if m := bukkitIndex.FindStringSubmatch(plain(first)); m != nil {
		pages, _ = strconv.Atoi(m[2])
	}
	out := first
	for page := 1; ; page++ {
		for _, m := range bukkitCommand.FindAllStringSubmatch(plain(out), -1) {
			name := m[1]
			if vanilla[name] || !usageLiteral.MatchString(name) {
				continue
			}
			if c, made := root.child(name); made {
				c.Executable = true
			}
		}
		if page >= pages || page >= bukkitPages {
			return nil
		}
		var err error
		if out, err = ask("help " + strconv.Itoa(page+1)); err != nil {
			return err
		}
	}
}

// serverCommands are the commands read from each running server's help,
// for the run of its game they were read from.
type serverCommands struct {
	mu sync.Mutex
	by map[string]*modCommands
}

type modCommands struct {
	instance string
	done     chan struct{}
	// Once done: what was read, and when to ask again if not all of it
	// could be.
	root  *usageNode
	retry time.Time
	// The game's tree with them, made once for the tree it was made from.
	merge  sync.Mutex
	base   string
	merged *commandTree
}

// modCommandsOf is the game's tree with the commands a server's mods and
// plugins add, read from its help if it is running; the tree as it is if
// they add none. pending is true while the help is still being read.
func (s *Server) modCommandsOf(ctx context.Context, mc *avsysv1.MinecraftServer, tree *commandTree) (out *commandTree, pending bool) {
	key := mc.Namespace + "/" + mc.Name
	c := &s.mods
	if mc.Status.Phase != avsysv1.PhaseRunning {
		c.mu.Lock()
		delete(c.by, key)
		c.mu.Unlock()
		return tree, false
	}
	instance := s.gameInstance(ctx, mc)
	c.mu.Lock()
	if c.by == nil {
		c.by = map[string]*modCommands{}
	}
	m := c.by[key]
	if m == nil || m.instance != instance || m.finished() && !m.retry.IsZero() && time.Now().After(m.retry) {
		m = &modCommands{instance: instance, done: make(chan struct{})}
		c.by[key] = m
		go s.readModCommands(m, mc.DeepCopy(), tree.names())
	}
	c.mu.Unlock()
	select {
	case <-m.done:
	case <-time.After(modsWait):
		return nil, true
	case <-ctx.Done():
		return nil, true
	}
	if m.root == nil || len(m.root.order) == 0 {
		return tree, false
	}
	m.merge.Lock()
	defer m.merge.Unlock()
	if m.base != tree.etag {
		gz, err := mergeCommands(tree.gz, m.root)
		if err != nil {
			s.Log.Warn("could not add a server's own commands to the game's", "server", mc.Name, "error", err)
			return tree, false
		}
		m.base, m.merged = tree.etag, newCommandTree(gz)
	}
	return m.merged, false
}

func (m *modCommands) finished() bool {
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}

// readModCommands reads a server's help, and notes what it read.
func (s *Server) readModCommands(m *modCommands, mc *avsysv1.MinecraftServer, vanilla map[string]bool) {
	ctx, cancel := context.WithTimeout(context.Background(), modsTimeout)
	defer cancel()
	var root *usageNode
	run, hangUp, err := s.gameSession(ctx, mc)
	if err == nil {
		root, err = readHelp(ctx, run, vanilla)
		hangUp()
	}
	if err != nil {
		s.Log.Warn("could not read the commands a server has from its help", "server", mc.Name, "error", err)
		m.retry = time.Now().Add(modsRetry)
	}
	m.root = root
	close(m.done)
}

// gameSession opens a way to run commands on a server's game one after
// another: RCON, or GameCommand where that is set. hangUp closes it.
func (s *Server) gameSession(ctx context.Context, mc *avsysv1.MinecraftServer) (run func(string) (string, error), hangUp func(), err error) {
	if s.GameCommand != nil {
		return func(command string) (string, error) { return s.GameCommand(ctx, mc, command) }, func() {}, nil
	}
	if s.Kube == nil || s.Dialer == nil {
		return nil, nil, errors.New("no way to reach the game")
	}
	secret, err := s.Kube.CoreV1().Secrets(mc.Namespace).Get(ctx, controller.RCONSecretName(mc.Name), metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	conn, err := s.Dialer.DialPod(ctx, mc.Namespace, mc.Name, controller.RCONPort)
	if err != nil {
		return nil, nil, err
	}
	// Tunnelled connections ignore deadlines: the time allowed is kept to
	// by closing.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	session, err := rcon.Login(conn, string(secret.Data["password"]))
	if err != nil {
		stop()
		conn.Close()
		return nil, nil, err
	}
	run = func(command string) (string, error) {
		out, err := session.Command(command)
		if err == nil {
			err = ctx.Err()
		}
		return out, err
	}
	return run, func() { stop(); conn.Close() }, nil
}

// names are the commands in a tree.
func (t *commandTree) names() map[string]bool {
	t.once.Do(func() {
		t.commands = map[string]bool{}
		raw, err := gunzip(t.gz)
		if err != nil {
			return
		}
		var root struct {
			Children map[string]json.RawMessage `json:"children"`
		}
		if json.Unmarshal(raw, &root) == nil {
			for name := range root.Children {
				t.commands[name] = true
			}
		}
	})
	return t.commands
}

func gunzip(gz []byte) ([]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(z, 64<<20))
}

// mergeCommands adds the commands read from a server's help to the game's
// tree, after the game's own, which keep their order. One the game has
// stays as the game has it.
func mergeCommands(gz []byte, mods *usageNode) ([]byte, error) {
	raw, err := gunzip(gz)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var have map[string]json.RawMessage
	if err := json.Unmarshal(top["children"], &have); err != nil {
		return nil, err
	}
	children := bytes.TrimSpace(top["children"])
	if len(children) < 2 || children[len(children)-1] != '}' {
		return nil, errors.New("the game's tree has no commands")
	}
	var b bytes.Buffer
	b.Write(children[:len(children)-1])
	for _, name := range mods.order {
		if _, ok := have[name]; ok {
			continue
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(name)
		v, err := mods.children[name].MarshalJSON()
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	top["children"] = b.Bytes()
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		kk, _ := json.Marshal(k)
		out.Write(kk)
		out.WriteByte(':')
		out.Write(top[k])
	}
	out.WriteByte('}')
	return packCommands(out.Bytes())
}
