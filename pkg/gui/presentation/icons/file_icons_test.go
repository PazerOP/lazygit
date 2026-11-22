package icons

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestFileIcons(t *testing.T) {
	t.Run("TestFileIcons", func(t *testing.T) {
		for name, icon := range nameIconMap {
			assert.Equal(t, 1, len([]rune(icon.Icon)), "nameIconMap[\"%s\"] is not a single rune", name)

		}

		for ext, icon := range extIconMap {
			assert.Equal(t, 1, len([]rune(icon.Icon)), "extIconMap[\"%s\"] is not a single rune", ext)

		}
	})
}
