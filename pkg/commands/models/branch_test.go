package models

import (
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBranchClone(t *testing.T) {
	branch := &Branch{}

	// Give every field a value other than its zero value, so that the test fails for a field that Clone forgets
	value := reflect.ValueOf(branch).Elem()
	for i := range value.NumField() {
		fieldName := value.Type().Field(i).Name
		switch field := value.Field(i).Addr().Interface().(type) {
		case *string:
			*field = fieldName
		case *bool:
			*field = true
		case *atomic.Int32:
			field.Store(7)
		case *atomic.Bool:
			field.Store(true)
		default:
			t.Fatalf("this test doesn't know how to set the field %s", fieldName)
		}
	}

	clone := branch.Clone()

	assert.NotSame(t, branch, clone)
	assert.Equal(t, branch, clone)
}
