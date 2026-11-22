package utils

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestThreadSafeMap(t *testing.T) {
	m := NewThreadSafeMap[int, int]()

	m.Set(1, 1)
	m.Set(2, 2)
	m.Set(3, 3)

	assert.Equal(t, 3, m.Len())

	assert.True(t, m.Has(1))

	assert.False(t, m.Has(4))

	_, ok := m.Get(1)
	assert.True(t, ok)

	_, ok = m.Get(4)
	assert.False(t, ok)

	m.Delete(1)

	assert.False(t, m.Has(1))

	m.Clear()

	assert.Equal(t, 0, m.Len())

}

func TestThreadSafeMapConcurrentReadWrite(t *testing.T) {
	m := NewThreadSafeMap[int, int]()

	go func() {
		for range 10000 {
			m.Set(0, 0)
		}
	}()

	for range 10000 {
		m.Get(0)
	}
}
