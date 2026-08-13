package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/config"
)

// Telling a stale DEFAULT cluster apart from a broken one (goal 21, O2).
//
// THE DEFECT. `runos config get` reported `cid: kqz` while `clusters list` returned five clusters,
// none of them `kqz`. Every command that falls back to the configured cluster targeted a cluster
// that is not in the account. The failure is quiet and bad: an agent that trusts the default
// operates on the wrong target, or gets an error it attributes to the wrong cause.
//
// The finding was hit twice. The second time the message was:
//
//	Cluster 'kqz' not found in account 'rjwrn'
//
// which reads as a broken CLUSTER rather than a stale SETTING. That is the whole harm: the sentence
// is true and points at the wrong thing, so the next move is to go and investigate a cluster that
// was never the problem.
//
// WHY HERE AND NOT AT EVERY CALL SITE. `GetDefaultClusterID` is consumed in a dozen commands plus
// the whole dynamic-command path. Annotating each one would be a dozen chances to miss one, and
// missing one leaves exactly the silent case this fixes. Every command's error passes through
// `Execute`, so one check covers all of them.

// staleClusterVerdict says whether a failure is explained by a stale default cluster.
type staleClusterVerdict int

const (
	// The failure is unrelated, or the cluster was named explicitly rather than defaulted.
	verdictNotStaleCluster staleClusterVerdict = iota
	// The command fell back to the configured default, and that cluster is not in the account.
	verdictDefaultClusterMissing
)

// judgeStaleCluster decides whether a stale default cluster explains this error.
//
// Pure, so the decision is testable without a network or a config file. It requires BOTH that the
// error is a cluster-not-found AND that the missing cluster is the configured default: a caller who
// passed `--cid` explicitly got exactly what they asked for and must not be told their config is
// wrong.
func judgeStaleCluster(errMsg, defaultCid string) staleClusterVerdict {
	if defaultCid == "" {
		return verdictNotStaleCluster
	}
	if !strings.Contains(errMsg, "not found in account") {
		return verdictNotStaleCluster
	}
	// The message names the cluster it could not find. Only claim staleness when that is the
	// configured default, quoted, so a substring of some other identifier cannot match.
	for _, quoted := range []string{"'" + defaultCid + "'", `"` + defaultCid + `"`} {
		if strings.Contains(errMsg, quoted) {
			return verdictDefaultClusterMissing
		}
	}
	return verdictNotStaleCluster
}

// explainStaleDefaultCluster prints guidance when a failure is really a stale configured default.
//
// Writes to stderr and never changes the exit code: the command still failed.
func explainStaleDefaultCluster(err error) {
	if err == nil {
		return
	}
	cfg, cerr := config.Load()
	if cerr != nil {
		return
	}
	defaultCid := cfg.GetDefaultClusterID()

	if judgeStaleCluster(err.Error(), defaultCid) != verdictDefaultClusterMissing {
		return
	}

	fmt.Fprintf(os.Stderr,
		"\nThat cluster came from your CONFIGURED DEFAULT, not from this command.\n"+
			"`%s` is no longer in this account, so the default is stale rather than the cluster broken.\n"+
			"Run `runos clusters list` to see what is there, then `runos clusters default <cid>` to point at one,\n"+
			"or pass `--cid <cid>` for a single command.\n", defaultCid)
}

// nullFlagName returns the flag a caller tried to clear by passing `null`, or "".
//
// GOAL 21, O11. Several numeric fields document three distinct instructions: a number sets a
// limit, `null` REMOVES it, and `0` is a real limit meaning nothing more may be created. The CLI
// renders them as int flags, and an int flag cannot carry null, so the documented instruction
// cannot be issued:
//
//	Error: invalid argument "null" for "--max-vcpus" flag: strconv.ParseInt: parsing "null": invalid syntax
//
// The dangerous part is that the plausible next guess is wrong in the WORST direction:
// `--max-vcpus 0` parses fine and does the opposite of what was wanted, freezing the group instead
// of unbounding it. So the caller who guessed RIGHT gets a parse error and the caller who guessed
// WRONG gets silent damage.
//
// Matched on pflag's own message. Narrow on purpose: only a literal `null` argument counts.
func nullFlagName(errMsg string) string {
	if !strings.Contains(errMsg, `invalid argument "null"`) {
		return ""
	}
	const marker = `for "`
	i := strings.Index(errMsg, marker)
	if i == -1 {
		return ""
	}
	rest := errMsg[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j == -1 {
		return ""
	}
	return rest[:j]
}

// explainNullNotAcceptedByFlag tells a caller who tried `--flag null` how to actually clear it.
//
// Writes to stderr and never changes the exit code.
func explainNullNotAcceptedByFlag(err error) {
	if err == nil {
		return
	}
	flag := nullFlagName(err.Error())
	if flag == "" {
		return
	}
	fmt.Fprintf(os.Stderr,
		"\nA flag cannot carry `null`, so clearing this value needs the file form:\n"+
			"  runos <command> ... -f body.yaml     with `<fieldName>: null` inside it\n"+
			"DO NOT pass `%s 0` instead. Zero is a real limit meaning nothing more may be\n"+
			"created, which is the opposite of removing the limit.\n", flag)
}
