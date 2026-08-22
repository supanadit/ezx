package scriptmodules

import "testing"

func TestEnvIsTruthyDefault(t *testing.T) {
	m := NewEnvModule()

	// Default-true when unset.
	if !m.IsTruthy("EZX_TEST_UNSET", "true") {
		t.Error("IsTruthy(unset, true) = false, want true (default-true)")
	}
	// Default-false when unset.
	if m.IsTruthy("EZX_TEST_UNSET2", "false") {
		t.Error("IsTruthy(unset, false) = true, want false")
	}
	// No default: unset is falsy.
	if m.IsTruthy("EZX_TEST_UNSET3") {
		t.Error("IsTruthy(unset) = true, want false")
	}

	// Set value overrides the default.
	t.Setenv("EZX_TEST_SET_TRUE", "false")
	if m.IsTruthy("EZX_TEST_SET_TRUE", "true") {
		t.Error("IsTruthy(set=false, default true) = true, want false (set overrides)")
	}
	t.Setenv("EZX_TEST_SET_FALSE", "1")
	if !m.IsTruthy("EZX_TEST_SET_FALSE", "false") {
		t.Error("IsTruthy(set=1, default false) = false, want true (set overrides)")
	}

	// Empty string env: treated as unset, default applies.
	t.Setenv("EZX_TEST_EMPTY", "")
	if !m.IsTruthy("EZX_TEST_EMPTY", "true") {
		t.Error("IsTruthy(empty, true) = false, want true (empty falls back to default)")
	}
}