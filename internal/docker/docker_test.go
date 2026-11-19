package docker

import (
	"reflect"
	"sort"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	cases := map[string][]string{
		`--health-cmd "pg_isready -U postgres" --health-interval 10s`: {"--health-cmd", "pg_isready -U postgres", "--health-interval", "10s"},
		`--health-cmd='redis-cli ping'`:                               {"--health-cmd=redis-cli ping"},
		`a\ b  c`:                                                     {"a b", "c"},
		`""`:                                                          {""},
		``:                                                            nil,
	}
	for in, want := range cases {
		got, err := SplitArgs(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("SplitArgs(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := SplitArgs(`--x "open`); err == nil {
		t.Error("expected error for unterminated quote")
	}
}

func TestEnvFlagsKeepValuesOutOfArgv(t *testing.T) {
	flags := envFlags(map[string]string{"TOKEN": "s3cret", "A": "1"})
	sort.Strings(flags)
	if !reflect.DeepEqual(flags, []string{"--env", "--env", "A", "TOKEN"}) {
		t.Errorf("flags = %q", flags)
	}
}
