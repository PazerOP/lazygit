package git_commands

import (
	"fmt"
	"maps"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jesseduffield/generics/set"
	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/jesseduffield/lazygit/pkg/commands/oscommands"
	"github.com/jesseduffield/lazygit/pkg/common"
	"github.com/jesseduffield/lazygit/pkg/utils"
	"github.com/samber/lo"
	"github.com/sasha-s/go-deadlock"
	"golang.org/x/sync/errgroup"
)

// context:
// we want to only show 'safe' branches (ones that haven't e.g. been deleted)
// which `git branch -a` gives us, but we also want the recency data that
// git reflog gives us.
// So we get the HEAD, then append get the reflog branches that intersect with
// our safe branches, then add the remaining safe branches, ensuring uniqueness
// along the way

// if we find out we need to use one of these functions in the git.go file, we
// can just pull them out of here and put them there and then call them from in here

type BranchLoaderConfigCommands interface {
	Branches(cmd oscommands.ICmdObjBuilder) map[string]*BranchConfig
}

type BranchInfo struct {
	RefName      string
	DisplayName  string // e.g. '(HEAD detached at 123asdf)'
	DetachedHead bool
	// The subject of the commit that a detached HEAD points at
	Subject string
}

// BranchLoader returns a list of Branch objects for the current repo
type BranchLoader struct {
	*common.Common
	*GitCommon
	cmd                  oscommands.ICmdObjBuilder
	getCurrentBranchInfo func() (BranchInfo, error)
	// Returns a function that tells whether a branch that is ahead of its upstream has commits of its own
	newLocalOnlyCommitsCheck func() func(*models.Branch) (bool, error)
	config                   BranchLoaderConfigCommands

	// The divergences that the last load needed. A divergence is a property of commits, so it never changes.
	divergences      map[commitPair]divergence
	divergencesMutex deadlock.Mutex
	// Held while git determines divergences, so that a second refresh waits for the results instead of asking git for the same ones.
	resolveDivergencesMutex sync.Mutex
}

func NewBranchLoader(
	cmn *common.Common,
	gitCommon *GitCommon,
	cmd oscommands.ICmdObjBuilder,
	getCurrentBranchInfo func() (BranchInfo, error),
	newLocalOnlyCommitsCheck func() func(*models.Branch) (bool, error),
	config BranchLoaderConfigCommands,
) *BranchLoader {
	return &BranchLoader{
		Common:                   cmn,
		GitCommon:                gitCommon,
		cmd:                      cmd,
		getCurrentBranchInfo:     getCurrentBranchInfo,
		newLocalOnlyCommitsCheck: newLocalOnlyCommitsCheck,
		config:                   config,
	}
}

// Load the list of branches for the current repo.
//
// Git has to walk through history to tell how far a branch has diverged from
// its upstream and from its push destination. This can take long. So that the
// branch list doesn't have to wait for it, the returned branches only have
// the divergences that are known without it. If any are missing, the second
// return value is a function that determines them. It returns the branches
// again, with a new model for each branch that it filled in.
func (self *BranchLoader) Load(
	reflogCommits []*models.Commit,
	oldBranches []*models.Branch,
) ([]*models.Branch, func() ([]*models.Branch, error), error) {
	branches, tips, trackedRefs, err := self.obtainBranches()
	if err != nil {
		return nil, nil, err
	}

	// Sorting the branches can run git commands too, so run the other commands that follow the listing at the same time
	var wg sync.WaitGroup
	var refTips map[string]string
	var refTipsErr error
	var configBranches map[string]*BranchConfig
	wg.Go(utils.SafeFunc(func() {
		refTips, refTipsErr = self.loadTipsOfTrackedRefs(tips, trackedRefs)
	}))
	wg.Go(utils.SafeFunc(func() {
		configBranches = self.config.Branches(self.cmd)
	}))

	switch self.UserConfig().Git.LocalBranchSortOrder {
	case "date":
		if err := sortRefsWithEqualDatesByAncestry(
			self.cmd, branches, (*models.Branch).FullRefName, tips,
		); err != nil {
			self.Log.Errorf("Failed to sort branches by ancestry: %v", err)
		}

	case "recency":
		reflogBranches := self.obtainReflogBranches(reflogCommits)
		// loop through reflog branches. If there is a match, merge them, then remove it from the branches and keep it in the reflog branches
		branchesWithRecency := make([]*models.Branch, 0)
	outer:
		for _, reflogBranch := range reflogBranches {
			for j, branch := range branches {
				if branch.Head {
					continue
				}
				if strings.EqualFold(reflogBranch.Name, branch.Name) {
					branch.Recency = reflogBranch.Recency
					branchesWithRecency = append(branchesWithRecency, branch)
					branches = utils.Remove(branches, j)
					continue outer
				}
			}
		}

		// Sort branches that don't have a recency value alphabetically
		// (we're really doing this for the sake of deterministic behaviour across git versions)
		slices.SortFunc(branches, func(a *models.Branch, b *models.Branch) int {
			return strings.Compare(a.Name, b.Name)
		})

		branches = utils.Prepend(branches, branchesWithRecency...)
	}

	wg.Wait()
	if refTipsErr != nil {
		return nil, nil, refTipsErr
	}

	foundHead := false
	for i, branch := range branches {
		if branch.Head {
			foundHead = true
			branch.Recency = "  *"
			branches = utils.Move(branches, i, 0)
			break
		}
	}
	if !foundHead {
		info, err := self.getCurrentBranchInfo()
		if err != nil {
			return nil, nil, err
		}
		branches = utils.Prepend(branches, &models.Branch{Name: info.RefName, DisplayName: info.DisplayName, Head: true, DetachedHead: info.DetachedHead, Subject: info.Subject, Recency: "  *"})
	}

	oldBranchesByName := lo.KeyBy(oldBranches, func(b *models.Branch) string {
		return b.Name
	})
	unresolved := []unresolvedBranch{}
	neededPairs := set.New[commitPair]()

	for _, branch := range branches {
		match := configBranches[branch.Name]
		if match != nil {
			branch.UpstreamRemote = match.Remote
			branch.UpstreamBranch = match.Merge
		}

		oldBranch := oldBranchesByName[branch.Name]

		// If the branch already existed, take over the values that are
		// determined in the background, to reduce flicker
		if oldBranch != nil {
			branch.BehindBaseBranch.Store(oldBranch.BehindBaseBranch.Load())
			branch.UpstreamRewritten.Store(oldBranch.UpstreamRewritten.Load())
		}

		refs, ok := trackedRefs[branch.FullRefName()]
		if !ok {
			// A detached head tracks nothing
			continue
		}
		tip := tips[branch.FullRefName()].hash
		pullPair, pushPair := self.setDivergences(branch, tip, refs, refTips, oldBranch)
		for _, pair := range []*commitPair{pullPair, pushPair} {
			if pair != nil {
				neededPairs.Add(*pair)
			}
		}
		if branch.DivergencePending {
			unresolved = append(unresolved, unresolvedBranch{
				branch: branch,
				pull:   self.pairIfUnknown(pullPair),
				push:   self.pairIfUnknown(pushPair),
			})
		}
	}

	self.forgetDivergencesExcept(neededPairs)

	if len(unresolved) == 0 {
		return branches, nil, nil
	}

	resolveDivergences := func() ([]*models.Branch, error) {
		return self.resolveDivergences(branches, unresolved)
	}
	return branches, resolveDivergences, nil
}

// Starts loading the values of the given branches that take too long to
// determine to make the branch list wait for them. Each of them is loaded on a
// worker, which calls renderFunc when it is done.
func (self *BranchLoader) LoadExtraInfo(
	branches []*models.Branch,
	mainBranches *MainBranches,
	onWorker func(func() error),
	renderFunc func(),
) {
	if self.UserConfig().Gui.ShowDivergenceFromBaseBranch != "none" {
		onWorker(func() error {
			return self.GetBehindBaseBranchValuesForAllBranches(branches, mainBranches, renderFunc)
		})
	}

	onWorker(func() error {
		return self.checkForRewrittenUpstreams(branches, renderFunc)
	})
}

// For each branch that has diverged from its upstream, determines whether the
// divergence comes from the upstream branch having been rewritten, and stores
// the answer in the branch. A branch that we can't determine it for keeps the
// answer "no", so that we don't offer anything we aren't sure about.
func (self *BranchLoader) checkForRewrittenUpstreams(branches []*models.Branch, renderFunc func()) error {
	t := time.Now()
	hasLocalOnlyCommits := self.newLocalOnlyCommitsCheck()
	errg := errgroup.Group{}
	// Each check runs git processes; a repository can have thousands of diverged branches
	errg.SetLimit(runtime.GOMAXPROCS(0))

	for _, branch := range branches {
		if !branch.IsAheadForPull() || !branch.IsBehindForPull() {
			branch.UpstreamRewritten.Store(false)
			continue
		}

		errg.Go(utils.SafeFuncWithError(func() error {
			hasLocalOnlyCommits, err := hasLocalOnlyCommits(branch)
			if err != nil {
				// Not worth bothering the user about; it only means that we
				// don't show this branch differently.
				self.Log.Errorf("Failed to check whether branch %s has commits of its own: %v", branch.Name, err)
			}
			branch.UpstreamRewritten.Store(err == nil && !hasLocalOnlyCommits)
			return nil
		}))
	}

	err := errg.Wait()
	self.Log.Debugf("time to check for rewritten upstreams for all branches: %s", time.Since(t))
	renderFunc()
	return err
}

func (self *BranchLoader) GetBehindBaseBranchValuesForAllBranches(
	branches []*models.Branch,
	mainBranches *MainBranches,
	renderFunc func(),
) error {
	mainBranchRefs := mainBranches.Get()
	if len(mainBranchRefs) == 0 {
		return nil
	}

	if self.version.IsAtLeast(2, 41, 0) {
		return self.getBehindBaseBranchValuesFast(branches, mainBranchRefs, renderFunc)
	}
	return self.getBehindBaseBranchValuesLegacy(branches, mainBranches, renderFunc)
}

func (self *BranchLoader) getBehindBaseBranchValuesLegacy(
	branches []*models.Branch,
	mainBranches *MainBranches,
	renderFunc func(),
) error {
	t := time.Now()
	errg := errgroup.Group{}

	for _, branch := range branches {
		errg.Go(utils.SafeFuncWithError(func() error {
			baseBranch, err := self.GetBaseBranch(branch, mainBranches)
			if err != nil {
				return err
			}
			behind := 0 // prime it in case something below fails
			if baseBranch != "" {
				output, err := self.cmd.New(
					NewGitCmd("rev-list").
						Arg("--left-right").
						Arg("--count").
						Arg(fmt.Sprintf("%s...%s", branch.FullRefName(), baseBranch)).
						ToArgv(),
				).DontLog().RunWithOutput()
				if err != nil {
					return err
				}
				// The format of the output is "<ahead>\t<behind>"
				aheadBehindStr := strings.Split(strings.TrimSpace(output), "\t")
				if len(aheadBehindStr) == 2 {
					if value, err := strconv.Atoi(aheadBehindStr[1]); err == nil {
						behind = value
					}
				}
			}
			branch.BehindBaseBranch.Store(int32(behind))
			return nil
		}))
	}

	err := errg.Wait()
	self.Log.Debugf("time to get behind base branch values for all branches (legacy): %s", time.Since(t))
	renderFunc()
	return err
}

func (self *BranchLoader) getBehindBaseBranchValuesFast(
	branches []*models.Branch,
	mainBranchRefs []string,
	renderFunc func(),
) error {
	t := time.Now()

	output, err := self.cmd.New(
		buildAheadBehindForEachRefArgs(mainBranchRefs, []string{"refs/heads"}),
	).DontLog().RunWithOutput()
	if err != nil {
		return err
	}

	parsed := parseAheadBehindForEachRefOutput(output, len(mainBranchRefs))
	branchByRef := lo.KeyBy(branches, (*models.Branch).FullRefName)

	for _, p := range parsed {
		if branch, ok := branchByRef[p.refName]; ok {
			behind := selectBehindForBranch(p.aheadBehinds)
			branch.BehindBaseBranch.Store(int32(behind))
			delete(branchByRef, p.refName)
		}
	}

	// Branches not in parse are default to 0
	for _, branch := range branchByRef {
		branch.BehindBaseBranch.Store(0)
	}

	self.Log.Debugf("time to get behind base branch values for all branches (fast): %s", time.Since(t))
	renderFunc()
	return nil
}

// Find the base branch for the given branch (i.e. the main branch that the
// given branch was forked off of)
//
// Note that this function may return an empty string even if the returned error
// is nil, e.g. when none of the configured main branches exist. This is not
// considered an error condition, so callers need to check both the returned
// error and whether the returned base branch is empty (and possibly react
// differently in both cases).
func (self *BranchLoader) GetBaseBranch(branch *models.Branch, mainBranches *MainBranches) (string, error) {
	mergeBase := mainBranches.GetMergeBase(branch.FullRefName())
	if mergeBase == "" {
		return "", nil
	}

	output, err := self.cmd.New(
		NewGitCmd("for-each-ref").
			Arg("--contains").
			Arg(mergeBase).
			Arg("--format=%(refname)").
			Arg(mainBranches.Get()...).
			ToArgv(),
	).DontLog().RunWithOutput()
	if err != nil {
		return "", err
	}
	trimmedOutput := strings.TrimSpace(output)
	split := strings.Split(trimmedOutput, "\n")
	if len(split) == 0 || split[0] == "" {
		return "", nil
	}
	return split[0], nil
}

// Returns the branches, along with the tip of each of them and the refs that
// it tracks, both keyed by full ref name. The branches don't have their
// divergences yet.
func (self *BranchLoader) obtainBranches() ([]*models.Branch, map[string]refTip, map[string]trackedRefs, error) {
	output, err := self.getRawBranches()
	if err != nil {
		return nil, nil, nil, err
	}

	trimmedOutput := strings.TrimSpace(output)
	outputLines := strings.Split(trimmedOutput, "\n")

	tips := make(map[string]refTip, len(outputLines))
	trackedRefsByBranch := make(map[string]trackedRefs, len(outputLines))
	branches := lo.FilterMap(outputLines, func(line string, _ int) (*models.Branch, bool) {
		if line == "" {
			return nil, false
		}

		split := strings.Split(line, "\x00")
		if len(split) != len(branchFields) {
			// Ignore line if it isn't separated into the expected number of parts
			// This is probably a warning message, for more info see:
			// https://github.com/jesseduffield/lazygit/issues/1385#issuecomment-885580439
			return nil, false
		}

		storeCommitDateAsRecency := self.UserConfig().Git.LocalBranchSortOrder != "recency"
		branch, tip, refs := obtainBranch(split, storeCommitDateAsRecency)
		tips[branch.FullRefName()] = tip
		trackedRefsByBranch[branch.FullRefName()] = refs
		return branch, true
	})

	return branches, tips, trackedRefsByBranch, nil
}

func (self *BranchLoader) getRawBranches() (string, error) {
	format := strings.Join(
		lo.Map(branchFields, func(thing string, _ int) string {
			return "%(" + thing + ")"
		}),
		"%00",
	)

	var sortOrder string
	switch strings.ToLower(self.UserConfig().Git.LocalBranchSortOrder) {
	case "recency", "date":
		sortOrder = "-committerdate"
	case "alphabetical":
		sortOrder = "refname"
	default:
		sortOrder = "refname"
	}

	cmdArgs := NewGitCmd("for-each-ref").
		Arg(fmt.Sprintf("--sort=%s", sortOrder)).
		Arg(fmt.Sprintf("--format=%s", format)).
		Arg("refs/heads").
		ToArgv()

	return self.cmd.New(cmdArgs).DontLog().RunWithOutput()
}

// The fields leave out %(upstream:track) and %(push:track). Git walks through
// history for each branch to fill them in.
var branchFields = []string{
	"HEAD",
	"refname",
	"upstream",
	"push",
	"subject",
	"objectname",
	"committerdate:unix",
}

// The refs that a branch is compared with, as full ref names
type trackedRefs struct {
	// The remote-tracking branch of the upstream. Empty if the branch has no upstream.
	upstream string
	// The remote-tracking branch that a push of the branch updates. Empty if git has no push destination for the branch.
	push string
}

// Obtain branch information from parsed line output of getRawBranches(). The
// branch doesn't have its divergences yet.
func obtainBranch(split []string, storeCommitDateAsRecency bool) (*models.Branch, refTip, trackedRefs) {
	headMarker := split[0]
	fullName := split[1]
	upstreamRef := split[2]
	pushRef := split[3]
	subject := split[4]
	commitHash := split[5]
	commitDate := split[6]

	name := strings.TrimPrefix(fullName, "refs/heads/")
	pushRemote, pushBranch := parsePushDestination(pushRef)

	recency := ""
	if storeCommitDateAsRecency {
		if unixTimestamp, err := strconv.ParseInt(commitDate, 10, 64); err == nil {
			recency = utils.UnixToTimeAgo(unixTimestamp)
		}
	}

	branch := &models.Branch{
		Name:       name,
		Recency:    recency,
		PushRemote: pushRemote,
		PushBranch: pushBranch,
		Head:       headMarker == "*",
		Subject:    subject,
		CommitHash: commitHash,
	}

	return branch,
		refTip{hash: commitHash, committerDate: commitDate},
		trackedRefs{upstream: upstreamRef, push: pushRef}
}

// Commits that are compared with each other
type commitPair struct {
	tip  string
	base string
}

// How many commits the tip of a commitPair has that the base doesn't have, and
// the other way round
type divergence struct {
	ahead  int
	behind int
}

type unresolvedBranch struct {
	branch *models.Branch
	// The commits that give the divergence from the upstream
	pull *commitPair
	// The commits that give the divergence from the push destination
	push *commitPair
}

// When more local branches than this are to be compared with the same commit, we ask git about all of them.
const maxRefNamesPerDivergenceCommand = 100

// Returns the tips of the refs that the branches track, keyed by full ref
// name. A ref that doesn't exist has no entry. tips holds the local branches.
func (self *BranchLoader) loadTipsOfTrackedRefs(
	tips map[string]refTip,
	trackedRefsByBranch map[string]trackedRefs,
) (map[string]string, error) {
	refTips := make(map[string]string, len(tips))
	for refName, tip := range tips {
		refTips[refName] = tip.hash
	}

	patterns := set.New[string]()
	for _, refs := range trackedRefsByBranch {
		for _, refName := range []string{refs.upstream, refs.push} {
			if refName != "" && !strings.HasPrefix(refName, "refs/heads/") {
				patterns.Add(patternForTrackedRef(refName))
			}
		}
	}
	if patterns.Len() == 0 {
		return refTips, nil
	}

	sortedPatterns := patterns.ToSlice()
	slices.Sort(sortedPatterns)
	cmdArgs := NewGitCmd("for-each-ref").
		Arg("--format=%(refname)%00%(objectname)").
		Arg(sortedPatterns...).
		ToArgv()

	err := self.cmd.New(cmdArgs).DontLog().RunAndProcessLines(func(line string) (bool, error) {
		if refName, hash, found := strings.Cut(line, "\x00"); found {
			refTips[refName] = hash
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	return refTips, nil
}

// Returns the for-each-ref pattern that we use to get the tip of the given
// ref. For a remote-tracking branch this is the pattern.
func patternForTrackedRef(refName string) string {
	parts := strings.SplitN(refName, "/", 4)
	if len(parts) == 4 && parts[0] == "refs" && parts[1] == "remotes" {
		return strings.Join(parts[:3], "/")
	}
	return refName
}

// Sets how far the branch has diverged from its upstream and from its push
// destination, as far as we know it without asking git. Where we don't, the
// branch keeps the value that it had before, and we return the commits to
// compare.
func (self *BranchLoader) setDivergences(
	branch *models.Branch,
	tip string,
	refs trackedRefs,
	refTips map[string]string,
	oldBranch *models.Branch,
) (*commitPair, *commitPair) {
	branch.AheadForPull, branch.BehindForPull = "?", "?"
	branch.AheadForPush, branch.BehindForPush = "?", "?"

	if refs.upstream == "" {
		// The branch has no upstream that we have a local version of.
		return nil, nil
	}

	var pullPair, pushPair *commitPair

	if upstreamTip, ok := refTips[refs.upstream]; !ok {
		branch.UpstreamGone = true
	} else {
		pullPair = &commitPair{tip: tip, base: upstreamTip}
		if divergence, ok := self.knownDivergence(*pullPair); ok {
			branch.AheadForPull = strconv.Itoa(divergence.ahead)
			branch.BehindForPull = strconv.Itoa(divergence.behind)
		} else {
			branch.DivergencePending = true
			if oldBranch != nil && oldBranch.FullUpstreamRefName() == branch.FullUpstreamRefName() {
				branch.AheadForPull, branch.BehindForPull = oldBranch.AheadForPull, oldBranch.BehindForPull
			}
		}
	}

	if refs.push == "" {
		branch.AheadForPush, branch.BehindForPush = "0", "0"
	} else if pushTip, ok := refTips[refs.push]; ok {
		pushPair = &commitPair{tip: tip, base: pushTip}
		if divergence, ok := self.knownDivergence(*pushPair); ok {
			branch.AheadForPush = strconv.Itoa(divergence.ahead)
			branch.BehindForPush = strconv.Itoa(divergence.behind)
		} else {
			branch.DivergencePending = true
			if oldBranch != nil && oldBranch.PushRemote == branch.PushRemote && oldBranch.PushBranch == branch.PushBranch {
				branch.AheadForPush, branch.BehindForPush = oldBranch.AheadForPush, oldBranch.BehindForPush
			}
		}
	}

	return pullPair, pushPair
}

func (self *BranchLoader) knownDivergence(pair commitPair) (divergence, bool) {
	if pair.tip == pair.base {
		return divergence{}, true
	}

	self.divergencesMutex.Lock()
	defer self.divergencesMutex.Unlock()

	result, ok := self.divergences[pair]
	return result, ok
}

// Returns the given pair if we don't know its divergence, and nil otherwise
func (self *BranchLoader) pairIfUnknown(pair *commitPair) *commitPair {
	if pair == nil {
		return nil
	}
	if _, ok := self.knownDivergence(*pair); ok {
		return nil
	}
	return pair
}

func (self *BranchLoader) rememberDivergences(divergences map[commitPair]divergence) {
	self.divergencesMutex.Lock()
	defer self.divergencesMutex.Unlock()

	if self.divergences == nil {
		self.divergences = make(map[commitPair]divergence, len(divergences))
	}
	maps.Copy(self.divergences, divergences)
}

// Keeps the cache from growing each time a branch or the ref that it tracks
// moves
func (self *BranchLoader) forgetDivergencesExcept(pairs *set.Set[commitPair]) {
	self.divergencesMutex.Lock()
	defer self.divergencesMutex.Unlock()

	maps.DeleteFunc(self.divergences, func(pair commitPair, _ divergence) bool {
		return !pairs.Includes(pair)
	})
}

// Determines the divergences that the given unresolved branches are missing.
// Returns the branches, with a new model in the place of each of the
// unresolved ones. The models that we were given stay untouched, because the
// UI is showing them by now.
func (self *BranchLoader) resolveDivergences(
	branches []*models.Branch,
	unresolved []unresolvedBranch,
) ([]*models.Branch, error) {
	self.resolveDivergencesMutex.Lock()
	defer self.resolveDivergencesMutex.Unlock()

	t := time.Now()

	// The local branches to compare with each commit
	refNamesByBase := map[string][]string{}
	pairs := set.New[commitPair]()
	for _, entry := range unresolved {
		for _, pair := range []*commitPair{entry.pull, entry.push} {
			if pair == nil {
				continue
			}
			if _, ok := self.knownDivergence(*pair); !ok {
				refNamesByBase[pair.base] = append(refNamesByBase[pair.base], entry.branch.FullRefName())
				pairs.Add(*pair)
			}
		}
	}

	if err := self.loadDivergences(pairs.ToSlice(), refNamesByBase); err != nil {
		return nil, err
	}

	resolvedByOriginal := make(map[*models.Branch]*models.Branch, len(unresolved))
	for _, entry := range unresolved {
		resolved := entry.branch.Clone()
		resolved.DivergencePending = false
		resolved.AheadForPull, resolved.BehindForPull = self.resolvedDivergence(
			entry.pull, entry.branch.AheadForPull, entry.branch.BehindForPull)
		resolved.AheadForPush, resolved.BehindForPush = self.resolvedDivergence(
			entry.push, entry.branch.AheadForPush, entry.branch.BehindForPush)
		resolvedByOriginal[entry.branch] = resolved
	}

	self.Log.Debugf("time to determine the divergences of %d branches: %s", len(unresolved), time.Since(t))

	return lo.Map(branches, func(branch *models.Branch, _ int) *models.Branch {
		if resolved, ok := resolvedByOriginal[branch]; ok {
			return resolved
		}
		return branch
	}), nil
}

// Returns the divergence of the given pair as the branch model holds it. nil
// stands for a divergence that was known from the start, so its values stay.
// A branch that moved while we were asking git about it has no result; its
// divergence is unknown until the next load.
func (self *BranchLoader) resolvedDivergence(pair *commitPair, ahead string, behind string) (string, string) {
	if pair == nil {
		return ahead, behind
	}
	if divergence, ok := self.knownDivergence(*pair); ok {
		return strconv.Itoa(divergence.ahead), strconv.Itoa(divergence.behind)
	}
	return "?", "?"
}

// Asks git how far the given local branches have diverged from the commits
// that they are listed under, and remembers the results
func (self *BranchLoader) loadDivergences(pairs []commitPair, refNamesByBase map[string][]string) error {
	if self.version.IsAtLeast(2, 41, 0) {
		return self.loadDivergencesFast(refNamesByBase)
	}
	return self.loadDivergencesLegacy(pairs)
}

// One git process per base commit. git walks through the history of all the
// branches that are compared with that commit once.
func (self *BranchLoader) loadDivergencesFast(refNamesByBase map[string][]string) error {
	errg := errgroup.Group{}
	errg.SetLimit(runtime.GOMAXPROCS(0))

	for base, refNames := range refNamesByBase {
		errg.Go(utils.SafeFuncWithError(func() error {
			refPatterns := lo.Uniq(refNames)
			if len(refPatterns) > maxRefNamesPerDivergenceCommand {
				refPatterns = []string{"refs/heads"}
			}

			cmdArgs := NewGitCmd("for-each-ref").
				Arg("--format=%(objectname)%00%(ahead-behind:" + base + ")").
				Arg(refPatterns...).
				ToArgv()
			output, err := self.cmd.New(cmdArgs).DontLog().RunWithOutput()
			if err != nil {
				return err
			}

			// We key the results by the commit that git found each branch at.
			divergences := map[commitPair]divergence{}
			for line := range strings.Lines(output) {
				tip, aheadBehindField, _ := strings.Cut(strings.TrimSpace(line), "\x00")
				if aheadBehind := parseAheadBehindField(aheadBehindField); aheadBehind.valid {
					divergences[commitPair{tip: tip, base: base}] = divergence{
						ahead: aheadBehind.ahead, behind: aheadBehind.behind,
					}
				}
			}
			self.rememberDivergences(divergences)
			return nil
		}))
	}

	return errg.Wait()
}

// One git process per branch and base commit, for git versions that don't
// have %(ahead-behind:...)
func (self *BranchLoader) loadDivergencesLegacy(pairs []commitPair) error {
	errg := errgroup.Group{}
	errg.SetLimit(runtime.GOMAXPROCS(0))

	for _, pair := range pairs {
		errg.Go(utils.SafeFuncWithError(func() error {
			output, err := self.cmd.New(
				NewGitCmd("rev-list").
					Arg("--left-right").
					Arg("--count").
					Arg(pair.tip + "..." + pair.base).
					ToArgv(),
			).DontLog().RunWithOutput()
			if err != nil {
				return err
			}

			// The format of the output is "<ahead>\t<behind>"
			if aheadBehind := parseAheadBehindField(output); aheadBehind.valid {
				self.rememberDivergences(map[commitPair]divergence{
					pair: {ahead: aheadBehind.ahead, behind: aheadBehind.behind},
				})
			}
			return nil
		}))
	}

	return errg.Wait()
}

// Splits the remote-tracking ref that the %(push) field names, e.g.
// refs/remotes/origin/main, into the remote and the remote branch. Returns
// empty strings if the field is empty because git has no push destination for
// the branch, or if the ref isn't under refs/remotes/.
func parsePushDestination(pushRef string) (string, string) {
	remoteAndBranch, ok := strings.CutPrefix(pushRef, "refs/remotes/")
	if !ok {
		return "", ""
	}

	// Remote names can't contain slashes, so the first one ends the remote name
	remote, branch, ok := strings.Cut(remoteAndBranch, "/")
	if !ok {
		return "", ""
	}

	return remote, branch
}

// TODO: only look at the new reflog commits, and otherwise store the recencies in
// int form against the branch to recalculate the time ago
func (self *BranchLoader) obtainReflogBranches(reflogCommits []*models.Commit) []*models.Branch {
	foundBranches := set.New[string]()
	re := regexp.MustCompile(`checkout: moving from ([\S]+) to ([\S]+)`)
	reflogBranches := make([]*models.Branch, 0, len(reflogCommits))

	for _, commit := range reflogCommits {
		match := re.FindStringSubmatch(commit.Name)
		if len(match) != 3 {
			continue
		}

		recency := utils.UnixToTimeAgo(commit.UnixTimestamp)
		for _, branchName := range match[1:] {
			if !foundBranches.Includes(branchName) {
				foundBranches.Add(branchName)
				reflogBranches = append(reflogBranches, &models.Branch{
					Recency: recency,
					Name:    branchName,
				})
			}
		}
	}
	return reflogBranches
}
