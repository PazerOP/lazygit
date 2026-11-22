package presentation

import (
	"fmt"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/commands/models"
	"github.com/jesseduffield/lazygit/pkg/config"
	"github.com/jesseduffield/lazygit/pkg/gui/types"
	"github.com/jesseduffield/lazygit/pkg/i18n"
	"github.com/jesseduffield/lazygit/pkg/utils"
)

func manyBranches(count int) []*models.Branch {
	branches := make([]*models.Branch, count)
	for i := range branches {
		branches[i] = &models.Branch{
			Name:           fmt.Sprintf("users/someone/feature-with-a-long-name-%04d", i),
			Recency:        "3d",
			UpstreamRemote: "origin",
			UpstreamBranch: fmt.Sprintf("users/someone/feature-with-a-long-name-%04d", i),
			AheadForPull:   "0",
			BehindForPull:  fmt.Sprint(i % 7),
			AheadForPush:   "0",
			BehindForPush:  "0",
			Subject:        "A commit subject of average length",
			CommitHash:     "0123456789abcdef0123456789abcdef01234567",
		}
	}
	return branches
}

func BenchmarkGetBranchListDisplayStrings(b *testing.B) {
	branches := manyBranches(6000)
	getItemOperation := func(types.HasUrn) types.ItemOperation { return types.ItemOperationNone }
	tr := i18n.EnglishTranslationSet()
	userConfig := config.GetDefaultConfig()

	b.Run("display strings", func(b *testing.B) {
		for b.Loop() {
			GetBranchListDisplayStrings(
				branches, getItemOperation, nil, false, "", 60, tr, userConfig, nil)
		}
	})

	b.Run("display strings and columns", func(b *testing.B) {
		for b.Loop() {
			displayStrings := GetBranchListDisplayStrings(
				branches, getItemOperation, nil, false, "", 60, tr, userConfig, nil)
			utils.RenderDisplayStrings(displayStrings, nil)
		}
	})
}
