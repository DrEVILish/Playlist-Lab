package config

import "testing"

// Node's config precedence is env var > persisted DB override > hardcoded
// default; this package only handles the env-var/default half (the DB
// override layer lives elsewhere), so these tests pin getEnv/getBool's own
// contract: an explicit env var always wins, and a missing or garbage value
// always falls back to the caller's default rather than zero-valuing it.
func TestGetEnv_OverridesDefault(t *testing.T) {
	t.Setenv("PLL_TEST_STR", "explicit")
	if got := getEnv("PLL_TEST_STR", "fallback"); got != "explicit" {
		t.Fatalf("getEnv with a set var: got %q, want %q", got, "explicit")
	}
}

func TestGetEnv_FallsBackWhenUnset(t *testing.T) {
	if got := getEnv("PLL_TEST_UNSET_STR", "fallback"); got != "fallback" {
		t.Fatalf("getEnv with no var: got %q, want %q", got, "fallback")
	}
}

func TestGetBool_ParsesExplicitValues(t *testing.T) {
	cases := map[string]bool{"true": true, "false": false, "1": true, "0": false, "TRUE": true}
	for raw, want := range cases {
		t.Setenv("PLL_TEST_BOOL", raw)
		if got := getBool("PLL_TEST_BOOL", !want); got != want {
			t.Errorf("getBool(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestGetBool_FallsBackWhenUnset(t *testing.T) {
	if got := getBool("PLL_TEST_BOOL_UNSET", true); got != true {
		t.Fatalf("getBool with no var: got %v, want true", got)
	}
	if got := getBool("PLL_TEST_BOOL_UNSET", false); got != false {
		t.Fatalf("getBool with no var: got %v, want false", got)
	}
}

// The bug this guards against: strconv.ParseBool errors on anything outside
// its known token set (true/false/1/0/t/f/...), and an unchecked error would
// zero-value the result to false regardless of the caller's requested
// default - silently flipping an "enabled by default" feature off for any
// admin who sets ENABLE_JOBS=yes instead of ENABLE_JOBS=true.
func TestGetBool_GarbageValueFallsBackToDefault(t *testing.T) {
	t.Setenv("PLL_TEST_BOOL_GARBAGE", "yes")
	if got := getBool("PLL_TEST_BOOL_GARBAGE", true); got != true {
		t.Errorf("garbage value with default=true: got %v, want true", got)
	}
	if got := getBool("PLL_TEST_BOOL_GARBAGE", false); got != false {
		t.Errorf("garbage value with default=false: got %v, want false", got)
	}
}
