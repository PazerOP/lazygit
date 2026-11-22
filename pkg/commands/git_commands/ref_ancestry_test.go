package git_commands

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/jesseduffield/lazygit/pkg/commands/oscommands"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

// A branch as the tests below describe it: a name, the hash of its tip, and the
// committer date of its tip
type testRef struct {
	name string
	hash string
	date string
}

func buildTestRefs(refs []testRef) ([]*models.Branch, map[string]refTip) {
	branches := lo.Map(refs, func(ref testRef, _ int) *models.Branch {
		return &models.Branch{Name: ref.name, CommitHash: ref.hash}
	})
	tips := make(map[string]refTip, len(refs))
	for _, ref := range refs {
		tips["refs/heads/"+ref.name] = refTip{hash: ref.hash, committerDate: ref.date}
	}
	return branches, tips
}

func branchNames(branches []*models.Branch) []string {
	return lo.Map(branches, func(branch *models.Branch, _ int) string {
		return branch.Name
	})
}

func TestGroupRefsWithEqualDates(t *testing.T) {
	scenarios := []struct {
		testName string
		refs     []testRef
		expected [][]string
	}{
		{
			testName: "no refs",
			refs:     []testRef{},
			expected: [][]string{},
		},
		{
			testName: "all dates distinct",
			refs: []testRef{
				{name: "a", hash: "1", date: "300"},
				{name: "b", hash: "2", date: "200"},
				{name: "c", hash: "3", date: "100"},
			},
			expected: [][]string{},
		},
		{
			testName: "all dates equal",
			refs: []testRef{
				{name: "a", hash: "1", date: "100"},
				{name: "b", hash: "2", date: "100"},
				{name: "c", hash: "3", date: "100"},
			},
			expected: [][]string{{"a", "b", "c"}},
		},
		{
			testName: "two groups, with a lone ref between and after them",
			refs: []testRef{
				{name: "a", hash: "1", date: "300"},
				{name: "b", hash: "2", date: "300"},
				{name: "c", hash: "3", date: "200"},
				{name: "d", hash: "4", date: "100"},
				{name: "e", hash: "5", date: "100"},
				{name: "f", hash: "6", date: "50"},
			},
			expected: [][]string{{"a", "b"}, {"d", "e"}},
		},
	}

	for _, s := range scenarios {
		t.Run(s.testName, func(t *testing.T) {
			branches, tips := buildTestRefs(s.refs)
			groups := groupRefsWithEqualDates(branches, (*models.Branch).FullRefName, tips)
			assert.Equal(t, s.expected, lo.Map(groups,
				func(group []*models.Branch, _ int) []string {
					return branchNames(group)
				}))
		})
	}
}

func TestSortRefsWithEqualDatesByAncestry(t *testing.T) {
	// A git command that the sort runs, and what it prints: one line for each
	// commit that is not older than the tips.
	type expectedCmd struct {
		args   []string
		output string
		err    error
	}

	// A stack of three branches, all committed within the same second, as git
	// returns them: sorted by name. bottom is the base of middle, which is the
	// base of top.
	stack := []testRef{
		{name: "middle", hash: "m", date: "100"},
		{name: "top", hash: "t", date: "100"},
		{name: "bottom", hash: "b", date: "100"},
	}

	stackArgs := []string{"rev-list", "--parents", "--max-age=100", "m", "t", "b"}

	scenarios := []struct {
		testName      string
		refs          []testRef
		expectedCmds  []expectedCmd
		expectedOrder []string
		expectedErr   string
	}{
		{
			testName: "a stack of branches is sorted from the top down",
			refs:     stack,
			expectedCmds: []expectedCmd{
				{args: stackArgs, output: "t m\nm b\nb old\n"},
			},
			expectedOrder: []string{"top", "middle", "bottom"},
		},
		{
			testName: "no two refs share a date, so nothing to do",
			refs: []testRef{
				{name: "middle", hash: "m", date: "300"},
				{name: "top", hash: "t", date: "200"},
				{name: "bottom", hash: "b", date: "100"},
			},
			expectedOrder: []string{"middle", "top", "bottom"},
		},
		{
			testName: "the command fails",
			refs:     stack,
			expectedCmds: []expectedCmd{
				{args: stackArgs, err: errors.New("fatal: bad object m")},
			},
			expectedOrder: []string{"middle", "top", "bottom"},
			expectedErr:   "fatal: bad object m",
		},
		{
			testName: "a branch unrelated to the stack is placed by name",
			refs: []testRef{
				{name: "middle", hash: "m", date: "100"},
				{name: "other", hash: "o", date: "100"},
				{name: "top", hash: "t", date: "100"},
				{name: "bottom", hash: "b", date: "100"},
			},
			expectedCmds: []expectedCmd{
				{
					args:   []string{"rev-list", "--parents", "--max-age=100", "m", "o", "t", "b"},
					output: "t m\nm b\no old\nb old\n",
				},
			},
			expectedOrder: []string{"other", "top", "middle", "bottom"},
		},
		{
			testName: "a stack stays together when a branch sorts into the middle of it",
			refs: []testRef{
				{name: "add-tests", hash: "t", date: "100"},
				{name: "cleanup", hash: "c", date: "100"},
				{name: "fix-parser", hash: "p", date: "100"},
			},
			expectedCmds: []expectedCmd{
				{
					args: []string{"rev-list", "--parents", "--max-age=100", "t", "c", "p"},
					// add-tests is based on fix-parser, and cleanup is on a line of its own, but its name sorts between both
					output: "t p\nc old\np old\n",
				},
			},
			expectedOrder: []string{"add-tests", "fix-parser", "cleanup"},
		},
		{
			testName: "a branch is descended from another one through commits that no branch points at",
			refs: []testRef{
				{name: "bottom", hash: "b", date: "100"},
				{name: "top", hash: "t", date: "100"},
			},
			expectedCmds: []expectedCmd{
				{
					args:   []string{"rev-list", "--parents", "--max-age=100", "b", "t"},
					output: "t 2\n2 1\n1 b\nb old\n",
				},
			},
			expectedOrder: []string{"top", "bottom"},
		},
		{
			testName: "a branch that merged two others comes before both",
			refs: []testRef{
				{name: "feature-a", hash: "a", date: "100"},
				{name: "feature-b", hash: "b", date: "100"},
				{name: "merged", hash: "m", date: "100"},
			},
			expectedCmds: []expectedCmd{
				{
					args:   []string{"rev-list", "--parents", "--max-age=100", "a", "b", "m"},
					output: "m a b\na old\nb old\n",
				},
			},
			expectedOrder: []string{"merged", "feature-a", "feature-b"},
		},
		{
			testName: "an ancestor behind an older commit is not found",
			refs: []testRef{
				{name: "bottom", hash: "b", date: "100"},
				{name: "top", hash: "t", date: "100"},
			},
			expectedCmds: []expectedCmd{
				{
					args: []string{"rev-list", "--parents", "--max-age=100", "b", "t"},
					// The parent of top is older than the tips, so git does not walk from it to bottom
					output: "t old\nb older\n",
				},
			},
			expectedOrder: []string{"bottom", "top"},
		},
		{
			testName: "each group is sorted on its own",
			refs: []testRef{
				{name: "middle", hash: "m", date: "200"},
				{name: "top", hash: "t", date: "200"},
				{name: "lone", hash: "l", date: "150"},
				{name: "base", hash: "a", date: "100"},
				{name: "derived", hash: "d", date: "100"},
			},
			expectedCmds: []expectedCmd{
				{
					args:   []string{"rev-list", "--parents", "--max-age=200", "m", "t"},
					output: "t m\nm old\n",
				},
				{
					args:   []string{"rev-list", "--parents", "--max-age=100", "a", "d"},
					output: "d a\na old\n",
				},
			},
			expectedOrder: []string{"top", "middle", "lone", "derived", "base"},
		},
		{
			testName: "a group whose branches are all on one commit is left alone",
			refs: []testRef{
				{name: "a", hash: "x", date: "100"},
				{name: "b", hash: "x", date: "100"},
			},
			expectedOrder: []string{"a", "b"},
		},
		{
			testName: "branches on the same commit are asked about once",
			refs: []testRef{
				{name: "mirror-1", hash: "s", date: "100"},
				{name: "mirror-2", hash: "s", date: "100"},
				{name: "stack-bottom", hash: "u", date: "100"},
				{name: "stack-top", hash: "t", date: "100"},
			},
			expectedCmds: []expectedCmd{
				{
					args:   []string{"rev-list", "--parents", "--max-age=100", "s", "u", "t"},
					output: "t u\ns old\nu old\n",
				},
			},
			expectedOrder: []string{"mirror-1", "mirror-2", "stack-top", "stack-bottom"},
		},
	}

	for _, s := range scenarios {
		t.Run(s.testName, func(t *testing.T) {
			runner := oscommands.NewFakeRunner(t)
			for _, cmd := range s.expectedCmds {
				runner.ExpectGitArgs(cmd.args, cmd.output, cmd.err)
			}
			gitCommon := buildGitCommon(commonDeps{runner: runner})

			branches, tips := buildTestRefs(s.refs)
			err := sortRefsWithEqualDatesByAncestry(gitCommon.cmd,
				branches, (*models.Branch).FullRefName, tips)

			if s.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, s.expectedErr)
			}
			assert.Equal(t, s.expectedOrder, branchNames(branches))
			runner.CheckForMissingCalls()
		})
	}
}

func TestSortRefsWithEqualDatesByAncestry_TooManyTips(t *testing.T) {
	refs := lo.Map(lo.Range(maxTipsForAncestrySorting+1), func(i int, _ int) testRef {
		return testRef{
			name: fmt.Sprintf("branch-%03d", i),
			hash: fmt.Sprintf("hash-%03d", i),
			date: "100",
		}
	})

	// The runner fails the test if the command runs at all
	runner := oscommands.NewFakeRunner(t)
	gitCommon := buildGitCommon(commonDeps{runner: runner})

	branches, tips := buildTestRefs(refs)
	err := sortRefsWithEqualDatesByAncestry(gitCommon.cmd,
		branches, (*models.Branch).FullRefName, tips)

	assert.NoError(t, err)
	assert.Equal(t, lo.Map(refs, func(ref testRef, _ int) string { return ref.name }),
		branchNames(branches))
	runner.CheckForMissingCalls()
}

// A repository with many remotes has the same branches under each of them, so
// a group can hold far more refs than the commits they point at
func TestSortRefsWithEqualDatesByAncestry_ManyRefsOnTwoTips(t *testing.T) {
	refs := lo.Map(lo.Range(4*maxTipsForAncestrySorting), func(i int, _ int) testRef {
		return testRef{
			name: fmt.Sprintf("branch-%03d", i),
			hash: lo.Ternary(i%2 == 0, "bottom", "top"),
			date: "100",
		}
	})

	runner := oscommands.NewFakeRunner(t).ExpectGitArgs(
		[]string{"rev-list", "--parents", "--max-age=100", "bottom", "top"},
		"top bottom\nbottom old\n", nil)
	gitCommon := buildGitCommon(commonDeps{runner: runner})

	branches, tips := buildTestRefs(refs)
	err := sortRefsWithEqualDatesByAncestry(gitCommon.cmd,
		branches, (*models.Branch).FullRefName, tips)

	assert.NoError(t, err)
	// the branches on the top commit first, each half still ordered by name
	expected := append(
		lo.FilterMap(refs, func(ref testRef, _ int) (string, bool) {
			return ref.name, ref.hash == "top"
		}),
		lo.FilterMap(refs, func(ref testRef, _ int) (string, bool) {
			return ref.name, ref.hash == "bottom"
		})...)
	assert.Equal(t, expected, branchNames(branches))
	runner.CheckForMissingCalls()
}
