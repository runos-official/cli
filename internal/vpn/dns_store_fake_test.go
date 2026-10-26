package vpn

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// fakeScutilStore stands in for `scutil` reading commands on stdin and talking to the dynamic
// store. It understands exactly the commands the daemon sends (d.init, d.add, set, remove, show,
// list) and prints them back in the shape scutil uses, so the production parsers run on realistic
// text. It records every set and remove, so a test can prove which keys a call touched.
type fakeScutilStore struct {
	// keys maps a store key to its dictionary: property -> rendered value lines.
	keys map[string]map[string][]string
	// log holds every set and remove the fake executed, in order.
	log []string
	// runs counts the scripts fed to it.
	runs int
	// err, when set, makes every run fail the way a missing or broken scutil does.
	err error
	// setErr makes set fail with scutil's own words and a failing exit.
	setErr error
	// ignoreSet makes set a silent no-op that still exits 0: scutil's exit status is not proof.
	ignoreSet bool
	// ignoreRemove does the same for remove.
	ignoreRemove bool
}

func newFakeScutilStore() *fakeScutilStore {
	return &fakeScutilStore{keys: map[string]map[string][]string{}}
}

// seed puts a foreign key into the store, as other software would.
func (s *fakeScutilStore) seed(key string, props map[string][]string) { s.keys[key] = props }

func (s *fakeScutilStore) has(key string) bool { _, ok := s.keys[key]; return ok }

func (s *fakeScutilStore) sortedKeys() []string {
	out := make([]string, 0, len(s.keys))
	for k := range s.keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// run is the seam the platform calls instead of starting scutil.
func (s *fakeScutilStore) run(script string) (string, error) {
	s.runs++
	if s.err != nil {
		return "", s.err
	}
	var out strings.Builder
	var dict map[string][]string
	for _, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		cmd, rest, _ := strings.Cut(line, " ")
		switch cmd {
		case "d.init":
			dict = map[string][]string{}
		case "d.add":
			if dict == nil {
				return out.String(), errors.New("d.add before d.init")
			}
			fields := strings.Fields(rest)
			if len(fields) < 2 {
				return out.String(), fmt.Errorf("bad d.add %q", line)
			}
			name, vals := fields[0], fields[1:]
			if vals[0] == "*" || vals[0] == "#" || vals[0] == "?" {
				kind := vals[0]
				vals = vals[1:]
				if kind == "*" {
					lines := []string{"<array> {"}
					for i, v := range vals {
						lines = append(lines, fmt.Sprintf("  %d : %s", i, v))
					}
					dict[name] = append(lines, "}")
					continue
				}
			}
			dict[name] = []string{strings.Join(vals, " ")}
		case "set":
			s.log = append(s.log, line)
			if s.setErr != nil {
				fmt.Fprintf(&out, "  %s\n", s.setErr)
				return out.String(), s.setErr
			}
			if !s.ignoreSet {
				s.keys[rest] = dict
			}
		case "remove":
			s.log = append(s.log, line)
			if !s.ignoreRemove {
				delete(s.keys, rest)
			}
		case "show":
			props, ok := s.keys[rest]
			if !ok {
				out.WriteString("  No such key\n")
				continue
			}
			out.WriteString(renderStoreDict(props))
		case "list":
			re, err := regexp.Compile(rest)
			if err != nil {
				return out.String(), err
			}
			n := 0
			for _, k := range s.sortedKeys() {
				if re.MatchString(k) {
					fmt.Fprintf(&out, "  subKey [%d] = %s\n", n, k)
					n++
				}
			}
			if n == 0 {
				out.WriteString("  no keys\n")
			}
		default:
			return out.String(), fmt.Errorf("unknown scutil command %q", cmd)
		}
	}
	return out.String(), nil
}

func renderStoreDict(props map[string][]string) string {
	var b strings.Builder
	b.WriteString("<dictionary> {\n")
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v := props[n]
		if len(v) > 1 {
			fmt.Fprintf(&b, "  %s : %s\n", n, v[0])
			for _, l := range v[1 : len(v)-1] {
				fmt.Fprintf(&b, "  %s\n", l)
			}
			b.WriteString("  }\n")
			continue
		}
		fmt.Fprintf(&b, "  %s : %s\n", n, strings.Join(v, " "))
	}
	b.WriteString("}\n")
	return b.String()
}

// setKeys returns the keys the fake was told to set, in order.
func (s *fakeScutilStore) setKeys() []string { return s.logKeys("set ") }

// removedKeys returns the keys the fake was told to remove, in order.
func (s *fakeScutilStore) removedKeys() []string { return s.logKeys("remove ") }

func (s *fakeScutilStore) logKeys(prefix string) []string {
	var out []string
	for _, l := range s.log {
		if strings.HasPrefix(l, prefix) {
			out = append(out, strings.TrimPrefix(l, prefix))
		}
	}
	return out
}
