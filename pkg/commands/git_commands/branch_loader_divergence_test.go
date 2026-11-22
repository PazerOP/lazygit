package git_commands

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/jesseduffield/lazygit/pkg/commands/oscommands"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

var listBranchesArgs = []string{
	"for-each-ref",
	"--sort=-committerdate",
	"--format=%(HEAD)%00%(refname)%00%(upstream)%00%(push)%00%(subject)%00%(objectname)%00%(committerdate:unix)",
	"refs/heads",
}

var branchConfigArgs = []string{"config", "--local", "--get-regexp", `^branch\.`}

// A line of the output of the command that lists the branches
func branchLine(head string, name string, upstream string, push string, hash string, date int) string {
	return strings.Join([]string{head, "refs/heads/" + name, upstream, push, "subject", hash, fmt.Sprint(date)}, "\x00") + "\n"
}

func buildBranchLoader(runner *oscommands.FakeCmdObjRunner, gitVersion *GitVersion) *BranchLoader {
	gitCommon := buildGitCommon(commonDeps{runner: runner, gitVersion: gitVersion})

	return &BranchLoader{
		Common:    gitCommon.Common,
		GitCommon: gitCommon,
		cmd:       gitCommon.cmd,
		config:    gitCommon.config,
	}
}

// What the tests look at in a branch
type branchDivergence struct {
	name          string
	aheadForPull  string
	behindForPull string
	aheadForPush  string
	behindForPush string
	pending       bool
	upstreamGone  bool
}

func branchDivergences(branches []*models.Branch) []branchDivergence {
	return lo.Map(branches, func(branch *models.Branch, _ int) branchDivergence {
		return branchDivergence{
			name:          branch.Name,
			aheadForPull:  branch.AheadForPull,
			behindForPull: branch.BehindForPull,
			aheadForPush:  branch.AheadForPush,
			behindForPush: branch.BehindForPush,
			pending:       branch.DivergencePending,
			upstreamGone:  branch.UpstreamGone,
		}
	})
}

func TestBranchLoaderDivergences(t *testing.T) {
	branchList := branchLine("*", "main", "refs/remotes/origin/main", "refs/remotes/origin/main", "aaa", 500) +
		branchLine("", "behind", "refs/remotes/origin/main", "", "bbb", 400) +
		branchLine("", "diverged", "refs/remotes/origin/diverged", "refs/remotes/fork/diverged", "ccc", 300) +
		branchLine("", "gone", "refs/remotes/origin/gone", "refs/remotes/origin/gone", "eee", 200) +
		branchLine("", "local", "", "", "fff", 100)

	trackedTipsArgs := []string{
		"for-each-ref", "--format=%(refname)%00%(objectname)",
		"refs/remotes/fork", "refs/remotes/origin",
	}
	trackedTips := "refs/remotes/origin/diverged\x00ddd\nrefs/remotes/origin/main\x00aaa\n"

	branchConfig := "branch.main.remote origin\nbranch.main.merge refs/heads/main\n" +
		"branch.behind.remote origin\nbranch.behind.merge refs/heads/main\n" +
		"branch.diverged.remote origin\nbranch.diverged.merge refs/heads/diverged\n" +
		"branch.gone.remote origin\nbranch.gone.merge refs/heads/gone\n"

	expectListing := func(runner *oscommands.FakeCmdObjRunner, branchList string, trackedTips string) {
		runner.
			ExpectGitArgs(listBranchesArgs, branchList, nil).
			ExpectGitArgs(trackedTipsArgs, trackedTips, nil).
			ExpectGitArgs(branchConfigArgs, branchConfig, nil)
	}

	// What we know without asking git to walk through history
	unresolved := []branchDivergence{
		{name: "main", aheadForPull: "0", behindForPull: "0", aheadForPush: "0", behindForPush: "0"},
		{name: "behind", aheadForPull: "?", behindForPull: "?", aheadForPush: "0", behindForPush: "0", pending: true},
		{name: "diverged", aheadForPull: "?", behindForPull: "?", aheadForPush: "?", behindForPush: "?", pending: true},
		{name: "gone", aheadForPull: "?", behindForPull: "?", aheadForPush: "?", behindForPush: "?", upstreamGone: true},
		{name: "local", aheadForPull: "?", behindForPull: "?", aheadForPush: "?", behindForPush: "?"},
	}
	resolved := []branchDivergence{
		unresolved[0],
		{name: "behind", aheadForPull: "0", behindForPull: "3", aheadForPush: "0", behindForPush: "0"},
		{name: "diverged", aheadForPull: "2", behindForPull: "5", aheadForPush: "?", behindForPush: "?"},
		unresolved[3],
		unresolved[4],
	}

	t.Run("divergences that need a walk through history are resolved afterwards", func(t *testing.T) {
		runner := oscommands.NewFakeRunner(t)
		loader := buildBranchLoader(runner, &GitVersion{2, 41, 0, ""})

		expectListing(runner, branchList, trackedTips)
		branches, resolveDivergences, err := loader.Load(nil, nil)
		assert.NoError(t, err)
		assert.Equal(t, unresolved, branchDivergences(branches))
		runner.CheckForMissingCalls()

		runner.
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:aaa)", "refs/heads/behind",
			}, "bbb\x000 3\n", nil).
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:ddd)", "refs/heads/diverged",
			}, "ccc\x002 5\n", nil)
		resolvedBranches, err := resolveDivergences()
		assert.NoError(t, err)
		assert.Equal(t, resolved, branchDivergences(resolvedBranches))
		runner.CheckForMissingCalls()

		// The UI is showing the branches that Load returned.
		assert.Equal(t, unresolved, branchDivergences(branches))
		assert.Same(t, branches[0], resolvedBranches[0])
		assert.NotSame(t, branches[1], resolvedBranches[1])

		// The next load knows the divergences as long as no ref moves
		expectListing(runner, branchList, trackedTips)
		branches, resolveDivergences, err = loader.Load(nil, resolvedBranches)
		assert.NoError(t, err)
		assert.Nil(t, resolveDivergences)
		assert.Equal(t, resolved, branchDivergences(branches))
		runner.CheckForMissingCalls()

		// A branch that moved shows its old values until its new ones.
		movedBranchList := strings.Replace(branchList, "bbb", "bbb2", 1)
		expectListing(runner, movedBranchList, trackedTips)
		branches, resolveDivergences, err = loader.Load(nil, branches)
		assert.NoError(t, err)
		assert.NotNil(t, resolveDivergences)
		assert.Equal(t,
			branchDivergence{name: "behind", aheadForPull: "0", behindForPull: "3", aheadForPush: "0", behindForPush: "0", pending: true},
			branchDivergences(branches)[1])
		runner.CheckForMissingCalls()
	})

	t.Run("a second load doesn't ask git again for what the first one is determining", func(t *testing.T) {
		runner := oscommands.NewFakeRunner(t)
		loader := buildBranchLoader(runner, &GitVersion{2, 41, 0, ""})

		expectListing(runner, branchList, trackedTips)
		_, resolveFirst, err := loader.Load(nil, nil)
		assert.NoError(t, err)
		expectListing(runner, branchList, trackedTips)
		_, resolveSecond, err := loader.Load(nil, nil)
		assert.NoError(t, err)

		runner.
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:aaa)", "refs/heads/behind",
			}, "bbb\x000 3\n", nil).
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:ddd)", "refs/heads/diverged",
			}, "ccc\x002 5\n", nil)
		_, err = resolveFirst()
		assert.NoError(t, err)
		runner.CheckForMissingCalls()

		// The runner fails the test if a command runs again
		resolvedBranches, err := resolveSecond()
		assert.NoError(t, err)
		assert.Equal(t, resolved, branchDivergences(resolvedBranches))
	})

	t.Run("a branch that moves while git determines its divergence stays unknown", func(t *testing.T) {
		runner := oscommands.NewFakeRunner(t)
		loader := buildBranchLoader(runner, &GitVersion{2, 41, 0, ""})

		expectListing(runner, branchList, trackedTips)
		_, resolveDivergences, err := loader.Load(nil, nil)
		assert.NoError(t, err)

		runner.
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:aaa)", "refs/heads/behind",
			}, "moved\x001 3\n", nil).
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:ddd)", "refs/heads/diverged",
			}, "ccc\x002 5\n", nil)
		resolvedBranches, err := resolveDivergences()
		assert.NoError(t, err)
		assert.Equal(t,
			branchDivergence{name: "behind", aheadForPull: "?", behindForPull: "?", aheadForPush: "0", behindForPush: "0"},
			branchDivergences(resolvedBranches)[1])
		runner.CheckForMissingCalls()
	})

	t.Run("a failing git command fails the resolution", func(t *testing.T) {
		runner := oscommands.NewFakeRunner(t)
		loader := buildBranchLoader(runner, &GitVersion{2, 41, 0, ""})

		expectListing(runner, branchList, trackedTips)
		_, resolveDivergences, err := loader.Load(nil, nil)
		assert.NoError(t, err)

		runner.
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:aaa)", "refs/heads/behind",
			}, "", errors.New("fatal: failed to find 'aaa'")).
			ExpectGitArgs([]string{
				"for-each-ref", "--format=%(objectname)%00%(ahead-behind:ddd)", "refs/heads/diverged",
			}, "ccc\x002 5\n", nil)
		_, err = resolveDivergences()
		assert.ErrorContains(t, err, "failed to find 'aaa'")
	})

	t.Run("git versions without %(ahead-behind) are asked about each pair of commits", func(t *testing.T) {
		runner := oscommands.NewFakeRunner(t)
		loader := buildBranchLoader(runner, &GitVersion{2, 40, 0, ""})

		expectListing(runner, branchList, trackedTips)
		_, resolveDivergences, err := loader.Load(nil, nil)
		assert.NoError(t, err)

		runner.
			ExpectGitArgs([]string{"rev-list", "--left-right", "--count", "bbb...aaa"}, "0\t3\n", nil).
			ExpectGitArgs([]string{"rev-list", "--left-right", "--count", "ccc...ddd"}, "2\t5\n", nil)
		resolvedBranches, err := resolveDivergences()
		assert.NoError(t, err)
		assert.Equal(t, resolved, branchDivergences(resolvedBranches))
		runner.CheckForMissingCalls()
	})
}

func TestBranchLoaderDivergences_ManyBranchesOnOneUpstream(t *testing.T) {
	numBranches := maxRefNamesPerDivergenceCommand + 1

	branchList := ""
	divergenceOutput := ""
	for i := range numBranches {
		branchList += branchLine("", fmt.Sprintf("branch-%03d", i), "refs/remotes/origin/main", "", fmt.Sprintf("hash-%03d", i), 1000-i)
		divergenceOutput += fmt.Sprintf("hash-%03d\x001 %d\n", i, i)
	}

	runner := oscommands.NewFakeRunner(t).
		ExpectGitArgs(listBranchesArgs, branchList, nil).
		ExpectGitArgs([]string{
			"for-each-ref", "--format=%(refname)%00%(objectname)", "refs/remotes/origin",
		}, "refs/remotes/origin/main\x00aaa\n", nil).
		ExpectGitArgs(branchConfigArgs, "", nil)
	loader := buildBranchLoader(runner, &GitVersion{2, 41, 0, ""})
	// The checked-out commit is not among the listed branches
	loader.getCurrentBranchInfo = func() (BranchInfo, error) {
		return BranchInfo{RefName: "abc", DisplayName: "(HEAD detached at abc)", DetachedHead: true}, nil
	}

	_, resolveDivergences, err := loader.Load(nil, nil)
	assert.NoError(t, err)

	// One name per branch would make git match every ref against every name
	runner.ExpectGitArgs([]string{
		"for-each-ref", "--format=%(objectname)%00%(ahead-behind:aaa)", "refs/heads",
	}, divergenceOutput, nil)
	resolvedBranches, err := resolveDivergences()
	assert.NoError(t, err)

	assert.Len(t, resolvedBranches, numBranches+1)
	assert.True(t, resolvedBranches[0].DetachedHead)
	for i, branch := range resolvedBranches[1:] {
		assert.Equal(t, "1", branch.AheadForPull)
		assert.Equal(t, fmt.Sprint(i), branch.BehindForPull)
		assert.False(t, branch.DivergencePending)
	}
	runner.CheckForMissingCalls()
}

func TestBranchLoaderDivergences_TrackedRefOutsideRemotes(t *testing.T) {
	branchList := branchLine("*", "main", "refs/heads/base", "refs/published/main", "aaa", 200) +
		branchLine("", "base", "", "", "bbb", 100)

	runner := oscommands.NewFakeRunner(t).
		ExpectGitArgs(listBranchesArgs, branchList, nil).
		ExpectGitArgs([]string{
			"for-each-ref", "--format=%(refname)%00%(objectname)", "refs/published/main",
		}, "refs/published/main\x00aaa\n", nil).
		ExpectGitArgs(branchConfigArgs, "", nil)
	loader := buildBranchLoader(runner, &GitVersion{2, 41, 0, ""})

	branches, resolveDivergences, err := loader.Load(nil, nil)
	assert.NoError(t, err)
	assert.NotNil(t, resolveDivergences)
	// The upstream is a local branch, whose tip the branch list has already
	assert.Equal(t, []branchDivergence{
		{name: "main", aheadForPull: "?", behindForPull: "?", aheadForPush: "0", behindForPush: "0", pending: true},
		{name: "base", aheadForPull: "?", behindForPull: "?", aheadForPush: "?", behindForPush: "?"},
	}, branchDivergences(branches))
	runner.CheckForMissingCalls()
}
