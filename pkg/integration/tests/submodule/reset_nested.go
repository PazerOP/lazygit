package submodule

import (
	"github.com/jesseduffield/lazygit/pkg/config"
	. "github.com/jesseduffield/lazygit/pkg/integration/components"
)

var ResetNested = NewIntegrationTest(NewIntegrationTestArgs{
	Description:  "Enter a submodule that contains a nested submodule with changes, and reset the nested submodule",
	ExtraCmdArgs: []string{},
	Skip:         false,
	SetupConfig:  func(cfg *config.AppConfig) {},
	SetupRepo: func(shell *Shell) {
		setupNestedSubmodules(shell)

		// Make changes inside the nested submodule so it shows as modified
		// when we enter the outer submodule
		shell.CreateFile("modules/outerSubPath/modules/innerSubPath/new_file", "content")
	},
	Run: func(t *TestDriver, keys config.KeybindingConfig) {
		assertInParentRepo := func() {
			t.Views().Status().Content(Contains("repo"))
		}
		assertInOuterSubmodule := func() {
			t.Views().Status().Content(Contains("outerSubPath"))
		}

		assertInParentRepo()

		// From the main repo, the outer submodule shows as modified (because
		// its nested submodule is dirty)
		t.Views().Files().Focus().
			Lines(
				Contains("modules").IsSelected(),
			).
			PressEnter().
			Lines(
				Equals("▼ modules").IsSelected(),
				Equals("   M outerSubPath (submodule)"),
			)

		// Enter the outer submodule
		t.Views().Submodules().Focus().
			Lines(
				Equals("outerSubName").IsSelected(),
				Equals("  - innerSubName"),
			).
			PressEnter()

		assertInOuterSubmodule()

		// Now we can see and reset the nested submodule
		t.Views().Files().Focus().
			Lines(
				Contains("modules").IsSelected(),
			).
			PressEnter().
			Lines(
				Equals("▼ modules").IsSelected(),
				Equals("   M innerSubPath (submodule)"),
			).
			NavigateToLine(Contains("innerSubPath (submodule)")).
			Press(keys.Universal.Remove).
			Tap(func() {
				t.ExpectPopup().Menu().
					Title(Equals("modules/innerSubPath")).
					Select(Contains("Stash uncommitted submodule changes and update")).
					Confirm()
			}).
			IsEmpty()

		// Verify the nested submodule was reset
		t.FileSystem().PathNotPresent("modules/innerSubPath/new_file")

		// Return to the parent repo
		t.Views().Files().PressEscape()
		assertInParentRepo()

		// The outer submodule should no longer be modified
		t.Views().Files().Focus().
			IsEmpty()
	},
})
