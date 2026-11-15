package controller

import (
	"net"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// KnownMod is a mod or plugin that listens on ports of its own. The panel
// opens them when the mod is added and closes them when the mod is taken
// off (see FitModPorts), and each start sets the mod up to listen on them
// and to tell players where they are reached (see modConfig).
//
// The panel's web app has the same list, in web/src/components/ExtraPorts.tsx.
type KnownMod struct {
	// Slug is its Modrinth slug, which a port opened for it names as what
	// it is for.
	Slug string
	// ID is its Modrinth project ID, which content found in an imported
	// server's files is listed by.
	ID    string
	Title string
	// Ports are the ones opened for it: those it listens on by default, or,
	// for one that by default shares the game's, a port of its own.
	Ports []avsysv1.ExtraPort
	// Map says it serves a web map on its port.
	Map bool
}

func modPort(name string, port int32, protocol corev1.Protocol) avsysv1.ExtraPort {
	return avsysv1.ExtraPort{Name: name, Port: port, Protocol: protocol}
}

// KnownMods are the mods and plugins the panel opens ports for. Floodgate
// needs none: it lets Bedrock players in through Geyser's.
var KnownMods = []KnownMod{
	{Slug: "geyser", ID: "wKkoqHrH", Title: "Geyser", Ports: []avsysv1.ExtraPort{
		modPort("geyser", 19132, corev1.ProtocolUDP),
		// Newer versions find Bedrock players through TCP on the same port
		// before they connect over UDP.
		modPort("geyser-tcp", 19132, corev1.ProtocolTCP),
	}},
	{Slug: "simple-voice-chat", ID: "9eGKb6K1", Title: "Simple Voice Chat", Ports: []avsysv1.ExtraPort{modPort("voice-chat", 24454, corev1.ProtocolUDP)}},
	// Its own default is the game's port, which is TCP alone through the router.
	{Slug: "plasmo-voice", ID: "1bZhdhsH", Title: "Plasmo Voice", Ports: []avsysv1.ExtraPort{modPort("plasmo-voice", 60606, corev1.ProtocolUDP)}},
	{Slug: "bluemap", ID: "swbUV1cr", Title: "BlueMap", Ports: []avsysv1.ExtraPort{modPort("bluemap", 8100, corev1.ProtocolTCP)}, Map: true},
	{Slug: "dynmap", ID: "fRQREgAc", Title: "Dynmap", Ports: []avsysv1.ExtraPort{modPort("dynmap", 8123, corev1.ProtocolTCP)}, Map: true},
	{Slug: "squaremap", ID: "PFb7ZqK6", Title: "squaremap", Ports: []avsysv1.ExtraPort{modPort("squaremap", 8080, corev1.ProtocolTCP)}, Map: true},
}

// KnownModOf is the known mod an entry of a server's content is, or nil.
func KnownModOf(m avsysv1.ContentRef) *KnownMod {
	if m.Source != avsysv1.SourceModrinth {
		return nil
	}
	for i := range KnownMods {
		if k := &KnownMods[i]; m.Project == k.Slug || m.Project == k.ID {
			return k
		}
	}
	return nil
}

// IsKnownMod says whether a port may say it was opened for this mod.
func IsKnownMod(slug string) bool {
	for _, k := range KnownMods {
		if k.Slug == slug {
			return true
		}
	}
	return false
}

func knownIn(mods []avsysv1.ContentRef) map[string]bool {
	out := map[string]bool{}
	for _, m := range mods {
		if k := KnownModOf(m); k != nil {
			out[k.Slug] = true
		}
	}
	return out
}

// FitModPorts is a server's extra ports once its content goes from before
// to after. A known mod added gets its ports, each unless the server has a
// port of that name or number already, or as many as it may have. Ports
// opened for a mod are closed with the mod. Ports of the server's own are
// left as they are, and so is a mod there before and after: one whose port
// was closed by hand stays without.
func FitModPorts(before, after []avsysv1.ContentRef, ports []avsysv1.ExtraPort) []avsysv1.ExtraPort {
	return FitModPortsLike(before, after, ports, nil)
}

// FitModPortsLike is FitModPorts where a mod added gets the ports opened
// for it in like, if there are any there, as a restore puts back what a
// snapshot had.
func FitModPortsLike(before, after []avsysv1.ContentRef, ports, like []avsysv1.ExtraPort) []avsysv1.ExtraPort {
	had, has := knownIn(before), knownIn(after)
	var out []avsysv1.ExtraPort
	for _, p := range ports {
		if p.For != "" && !has[p.For] && IsKnownMod(p.For) {
			continue
		}
		out = append(out, p)
	}
	for _, k := range KnownMods {
		if !has[k.Slug] || had[k.Slug] {
			continue
		}
		var add []avsysv1.ExtraPort
		for _, p := range like {
			if p.For == k.Slug {
				add = append(add, p)
			}
		}
		if len(add) == 0 {
			for _, p := range k.Ports {
				p.For = k.Slug
				add = append(add, p)
			}
		}
		for _, a := range add {
			taken := len(out) >= 8
			for _, p := range out {
				if p.Name == a.Name || (p.Port == a.Port && protocolOf(p) == protocolOf(a)) {
					taken = true
				}
			}
			if !taken {
				out = append(out, a)
			}
		}
	}
	return out
}

// modPortsEnv tells the start script which ports were opened for mods, a
// line each: the mod, the port it listens on and its protocol, and where
// players reach it, as the public port and host:port, 0 and - while that
// is not known.
func modPortsEnv(mc *avsysv1.MinecraftServer, status []avsysv1.PortStatus) string {
	var lines []string
	for _, p := range mc.Spec.Ports {
		if !IsKnownMod(p.For) {
			continue
		}
		public, address := int32(0), "-"
		for _, st := range status {
			if st.Name == p.Name && st.Port == p.Port && st.Message == "" && st.PublicPort != 0 && st.Address != "" {
				if _, port, err := net.SplitHostPort(st.Address); err == nil && port == strconv.Itoa(int(st.PublicPort)) {
					public, address = st.PublicPort, st.Address
				}
			}
		}
		lines = append(lines, strings.Join([]string{p.For, strconv.Itoa(int(p.Port)), string(protocolOf(p)), strconv.Itoa(int(public)), address}, " "))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
