package git_commands

// "*|feat/detect-purge|origin/feat/detect-purge|[ahead 1]"
import (
	"strconv"
	"testing"
	"time"

	"github.com/go-errors/errors"
	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/jesseduffield/lazygit/pkg/commands/oscommands"
	"github.com/sasha-s/go-deadlock"
	"github.com/stretchr/testify/assert"
)

func TestObtainBranch(t *testing.T) {
	type scenario struct {
		testName                 string
		input                    []string
		storeCommitDateAsRecency bool
		expectedBranch           *models.Branch
		expectedRefs             trackedRefs
	}

	// Use a time stamp of 2 1/2 hours ago, resulting in a recency string of "2h"
	now := time.Now().Unix()
	timeStamp := strconv.Itoa(int(now - 2.5*60*60))

	scenarios := []scenario{
		{
			testName:                 "NameWithSlashes",
			input:                    []string{"", "refs/heads/feature/a_branch", "", "", "subject", "123", timeStamp},
			storeCommitDateAsRecency: false,
			expectedBranch: &models.Branch{
				Name:       "feature/a_branch",
				Head:       false,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
		{
			testName:                 "NoUpstream",
			input:                    []string{"", "refs/heads/a_branch", "", "", "subject", "123", timeStamp},
			storeCommitDateAsRecency: false,
			expectedBranch: &models.Branch{
				Name:       "a_branch",
				Head:       false,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
		{
			testName:                 "IsHead",
			input:                    []string{"*", "refs/heads/a_branch", "", "", "subject", "123", timeStamp},
			storeCommitDateAsRecency: false,
			expectedBranch: &models.Branch{
				Name:       "a_branch",
				Head:       true,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
		{
			testName:                 "PushDestinationIsUpstream",
			input:                    []string{"", "refs/heads/a_branch", "refs/remotes/a_remote/a_branch", "refs/remotes/a_remote/a_branch", "subject", "123", timeStamp},
			storeCommitDateAsRecency: false,
			expectedRefs: trackedRefs{
				upstream: "refs/remotes/a_remote/a_branch",
				push:     "refs/remotes/a_remote/a_branch",
			},
			expectedBranch: &models.Branch{
				Name:       "a_branch",
				PushRemote: "a_remote",
				PushBranch: "a_branch",
				Head:       false,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
		{
			testName:                 "PushDestinationDiffersFromUpstream",
			input:                    []string{"", "refs/heads/a_branch", "refs/remotes/a_remote/a_branch", "refs/remotes/my_fork/feature/a_branch", "subject", "123", timeStamp},
			storeCommitDateAsRecency: false,
			expectedRefs: trackedRefs{
				upstream: "refs/remotes/a_remote/a_branch",
				push:     "refs/remotes/my_fork/feature/a_branch",
			},
			expectedBranch: &models.Branch{
				Name:       "a_branch",
				PushRemote: "my_fork",
				PushBranch: "feature/a_branch",
				Head:       false,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
		{
			testName:                 "PushDestinationNotARemoteTrackingRef",
			input:                    []string{"", "refs/heads/a_branch", "refs/remotes/a_remote/a_branch", "refs/published/a_branch", "subject", "123", timeStamp},
			storeCommitDateAsRecency: false,
			expectedRefs: trackedRefs{
				upstream: "refs/remotes/a_remote/a_branch",
				push:     "refs/published/a_branch",
			},
			expectedBranch: &models.Branch{
				Name:       "a_branch",
				Head:       false,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
		{
			testName:                 "UpstreamIsLocalBranch",
			input:                    []string{"", "refs/heads/a_branch", "refs/heads/other_branch", "", "subject", "123", timeStamp},
			storeCommitDateAsRecency: false,
			expectedRefs:             trackedRefs{upstream: "refs/heads/other_branch"},
			expectedBranch: &models.Branch{
				Name:       "a_branch",
				Head:       false,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
		{
			testName:                 "WithCommitDateAsRecency",
			input:                    []string{"", "refs/heads/a_branch", "", "", "subject", "123", timeStamp},
			storeCommitDateAsRecency: true,
			expectedBranch: &models.Branch{
				Name:       "a_branch",
				Recency:    "2h",
				Head:       false,
				Subject:    "subject",
				CommitHash: "123",
			},
		},
	}

	for _, s := range scenarios {
		t.Run(s.testName, func(t *testing.T) {
			branch, tip, refs := obtainBranch(s.input, s.storeCommitDateAsRecency)
			assert.EqualValues(t, s.expectedBranch, branch)
			assert.Equal(t, refTip{hash: "123", committerDate: timeStamp}, tip)
			assert.Equal(t, s.expectedRefs, refs)
		})
	}
}

func TestLoadWhenListingBranchesFails(t *testing.T) {
	runner := oscommands.NewFakeRunner(t).
		ExpectFunc("git for-each-ref", func(cmdObj *oscommands.CmdObj) bool {
			return cmdObj.GetCmd().Args[1] == "for-each-ref"
		}, "", errors.New("chdir /path/to/repo: no such file or directory"))

	gitCommon := buildGitCommon(commonDeps{runner: runner})

	loader := &BranchLoader{
		Common:    gitCommon.Common,
		GitCommon: gitCommon,
		cmd:       gitCommon.cmd,
	}

	_, _, err := loader.Load(nil, nil)
	assert.EqualError(t, err, "chdir /path/to/repo: no such file or directory")

	runner.CheckForMissingCalls()
}

func TestGetBehindBaseBranchValuesForAllBranches_FastPath(t *testing.T) {
	mainBranchRefs := []string{"refs/heads/master", "refs/remotes/origin/develop"}

	// Two branches: feat-x has clear divergence from develop; main matches master exactly.
	branches := []*models.Branch{
		{Name: "feat-x"},
		{Name: "main"},
	}

	expectedFormat := "%(refname)%00%(ahead-behind:refs/heads/master)%00%(ahead-behind:refs/remotes/origin/develop)"
	output := "refs/heads/feat-x\x0055 0\x005 5\n" + // picks develop (ahead=5 < 55), behind=5
		"refs/heads/main\x000 0\x000 0\n" // picks master (first, tie), behind=0

	runner := oscommands.NewFakeRunner(t).
		ExpectGitArgs([]string{"for-each-ref", "--format=" + expectedFormat, "refs/heads"}, output, nil)

	gitCommon := buildGitCommon(commonDeps{
		runner:     runner,
		gitVersion: &GitVersion{2, 41, 0, ""},
	})

	loader := &BranchLoader{
		Common:    gitCommon.Common,
		GitCommon: gitCommon,
		cmd:       gitCommon.cmd,
	}

	mainBranches := &MainBranches{
		c:                    gitCommon.Common,
		cmd:                  gitCommon.cmd,
		existingMainBranches: mainBranchRefs,
		previousMainBranches: gitCommon.Common.UserConfig().Git.MainBranches,
	}

	rendered := false
	err := loader.GetBehindBaseBranchValuesForAllBranches(branches, mainBranches, func() { rendered = true })
	assert.NoError(t, err)
	assert.True(t, rendered, "renderFunc should have been called")

	assert.Equal(t, int32(5), branches[0].BehindBaseBranch.Load(), "feat-x should be behind develop by 5")
	assert.Equal(t, int32(0), branches[1].BehindBaseBranch.Load(), "main should be behind master by 0")

	runner.CheckForMissingCalls()
}

// edge case where a failure would leave artifacts from prior load
func TestGetBehindBaseBranchValuesForAllBranches_FastPath_ClearsStaleValueWhenBranchMissingFromOutput(t *testing.T) {
	mainBranchRefs := []string{"refs/heads/master"}

	feat := &models.Branch{Name: "feat-x"}
	feat.BehindBaseBranch.Store(99) // stale value from a prior load
	ghost := &models.Branch{Name: "ghost"}
	ghost.BehindBaseBranch.Store(42) // stale value from a prior load

	expectedFormat := "%(refname)%00%(ahead-behind:refs/heads/master)"
	output := "refs/heads/feat-x\x003 5\n" // ghost is intentionally absent

	runner := oscommands.NewFakeRunner(t).
		ExpectGitArgs([]string{"for-each-ref", "--format=" + expectedFormat, "refs/heads"}, output, nil)

	gitCommon := buildGitCommon(commonDeps{
		runner:     runner,
		gitVersion: &GitVersion{2, 41, 0, ""},
	})

	loader := &BranchLoader{
		Common:    gitCommon.Common,
		GitCommon: gitCommon,
		cmd:       gitCommon.cmd,
	}

	mainBranches := &MainBranches{
		c:                    gitCommon.Common,
		cmd:                  gitCommon.cmd,
		existingMainBranches: mainBranchRefs,
		previousMainBranches: gitCommon.Common.UserConfig().Git.MainBranches,
	}

	err := loader.GetBehindBaseBranchValuesForAllBranches(
		[]*models.Branch{feat, ghost}, mainBranches, func() {})
	assert.NoError(t, err)

	assert.Equal(t, int32(5), feat.BehindBaseBranch.Load(), "feat-x should be updated to fresh value")
	assert.Equal(t, int32(0), ghost.BehindBaseBranch.Load(), "ghost should be reset to 0 since it has no fresh data")

	runner.CheckForMissingCalls()
}

func TestGetBehindBaseBranchValuesForAllBranches_LegacyPath(t *testing.T) {
	mainBranchRefs := []string{"refs/heads/master"}

	branches := []*models.Branch{
		{Name: "feat-x"},
	}

	// In legacy path: per-branch GetBaseBranch (merge-base + for-each-ref --contains)
	// then rev-list --left-right --count.
	runner := oscommands.NewFakeRunner(t).
		ExpectGitArgs([]string{"merge-base", "refs/heads/feat-x", "refs/heads/master"}, "abc123\n", nil).
		ExpectGitArgs([]string{"for-each-ref", "--contains", "abc123", "--format=%(refname)", "refs/heads/master"}, "refs/heads/master\n", nil).
		ExpectGitArgs([]string{"rev-list", "--left-right", "--count", "refs/heads/feat-x...refs/heads/master"}, "5\t7\n", nil)

	gitCommon := buildGitCommon(commonDeps{
		runner:     runner,
		gitVersion: &GitVersion{2, 34, 0, ""}, // pre-2.41, forces legacy
	})

	loader := &BranchLoader{
		Common:    gitCommon.Common,
		GitCommon: gitCommon,
		cmd:       gitCommon.cmd,
	}

	mainBranches := &MainBranches{
		c:                    gitCommon.Common,
		cmd:                  gitCommon.cmd,
		existingMainBranches: mainBranchRefs,
		previousMainBranches: gitCommon.Common.UserConfig().Git.MainBranches,
	}

	rendered := false
	err := loader.GetBehindBaseBranchValuesForAllBranches(branches, mainBranches, func() { rendered = true })
	assert.NoError(t, err)
	assert.True(t, rendered)
	assert.Equal(t, int32(7), branches[0].BehindBaseBranch.Load())

	runner.CheckForMissingCalls()
}

func TestCheckForRewrittenUpstreams(t *testing.T) {
	branch := func(name string, ahead string, behind string) *models.Branch {
		return &models.Branch{
			Name:           name,
			UpstreamRemote: "origin",
			UpstreamBranch: name,
			AheadForPull:   ahead,
			BehindForPull:  behind,
		}
	}

	notDiverged := branch("not-diverged", "0", "2")
	rewritten := branch("rewritten", "3", "5")
	ownCommits := branch("own-commits", "3", "5")
	failing := branch("failing", "1", "1")

	// A branch that is no longer diverged must lose the value it had before
	notDiverged.UpstreamRewritten.Store(true)

	branches := []*models.Branch{notDiverged, rewritten, ownCommits, failing}

	var mutex deadlock.Mutex
	queried := []string{}
	hasLocalOnlyCommits := func(branch *models.Branch) (bool, error) {
		mutex.Lock()
		queried = append(queried, branch.Name)
		mutex.Unlock()

		switch branch.Name {
		case "own-commits":
			return true, nil
		case "failing":
			return false, errors.New("error")
		default:
			return false, nil
		}
	}

	gitCommon := buildGitCommon(commonDeps{})
	loader := &BranchLoader{
		Common:    gitCommon.Common,
		GitCommon: gitCommon,
		cmd:       gitCommon.cmd,
		newLocalOnlyCommitsCheck: func() func(*models.Branch) (bool, error) {
			return hasLocalOnlyCommits
		},
	}

	rendered := false
	err := loader.checkForRewrittenUpstreams(branches, func() { rendered = true })
	assert.NoError(t, err)
	assert.True(t, rendered, "renderFunc should have been called")

	assert.ElementsMatch(t, []string{"rewritten", "own-commits", "failing"}, queried,
		"only diverged branches should be looked at")
	assert.False(t, notDiverged.UpstreamRewritten.Load())
	assert.True(t, rewritten.UpstreamRewritten.Load())
	assert.False(t, ownCommits.UpstreamRewritten.Load())
	assert.False(t, failing.UpstreamRewritten.Load(), "a failed check should not claim anything")
}
