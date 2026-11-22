package utils

import (
	"bytes"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestOnceWriter(t *testing.T) {
	innerWriter := bytes.NewBuffer(nil)
	counter := 0
	onceWriter := NewOnceWriter(innerWriter, func() {
		counter++
	})
	_, _ = onceWriter.Write([]byte("hello"))
	_, _ = onceWriter.Write([]byte("hello"))
	assert.Equal(t, 1, counter)

}
