//go:build !native_capacity

package nativepty

import (
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

type capacityFixture struct{ failed bool }

func capacityHelperBuildArgs() []string { return nil }
func prepareCapacityFixture(*testing.T, *Backend, string, *domain.LaunchSpec) *capacityFixture {
	return &capacityFixture{}
}
func (*capacityFixture) begin(time.Time)                                 {}
func (*capacityFixture) poll()                                           {}
func (*capacityFixture) collect(*testing.T, *Backend, domain.RuntimeRef) {}
