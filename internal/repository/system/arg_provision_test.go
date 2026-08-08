package system

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/envutil"
)

func TestBuildArgsInterpolationDefault(t *testing.T) {
	p := domain.Process{
		Arguments: []string{
			"--web.listen-address=:${PROMETHEUS_PORT:-9090}",
			"--config.file=${PROMETHEUS_CONFIG_FILE:-/etc/prometheus.yml}",
		},
	}
	got, err := buildArgs(p, nil)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--web.listen-address=:9090", "--config.file=/etc/prometheus.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsInterpolationFromEnv(t *testing.T) {
	env := []string{"PROMETHEUS_PORT=12345"}
	p := domain.Process{
		Arguments: []string{"--web.listen-address=:${PROMETHEUS_PORT:-9090}"},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--web.listen-address=:12345"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsInterpolationEmptyVarUsesDefault(t *testing.T) {
	env := []string{"PROMETHEUS_PORT="}
	p := domain.Process{
		Arguments: []string{"--port=${PROMETHEUS_PORT:-9090}"},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--port=9090"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsIfSetValue(t *testing.T) {
	env := []string{"PROMETHEUS_WEB_CONFIG_FILE=/etc/web.yml"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:    "--web.config.file",
				FromEnv: "PROMETHEUS_WEB_CONFIG_FILE",
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--web.config.file=/etc/web.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsIfSetValueUnset(t *testing.T) {
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{Flag: "--web.config.file", FromEnv: "PROMETHEUS_WEB_CONFIG_FILE"},
		},
	}
	got, err := buildArgs(p, nil)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("args = %v, want none", got)
	}
}

func TestBuildArgsIfTruthyFlag(t *testing.T) {
	env := []string{"PROMETHEUS_ENABLE_WEB_LIFECYCLE=true"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				When:   domain.EnvCondition{Name: "PROMETHEUS_ENABLE_WEB_LIFECYCLE", Value: "true"},
				Flag:   "--web.enable-lifecycle",
				Format: domain.ArgFormatBareFlag,
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--web.enable-lifecycle"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsIfTruthyFlagNotSet(t *testing.T) {
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				When:   domain.EnvCondition{Name: "PROMETHEUS_ENABLE_WEB_LIFECYCLE", Value: "true"},
				Flag:   "--web.enable-lifecycle",
				Format: domain.ArgFormatBareFlag,
			},
		},
	}
	got, err := buildArgs(p, nil)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("args = %v, want none", got)
	}
}

func TestBuildArgsIfTruthyValue(t *testing.T) {
	env := []string{"ETCD_CLIENT_CERT_AUTH=true"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				When:   domain.EnvCondition{Name: "ETCD_CLIENT_CERT_AUTH", Value: "true"},
				Flag:   "--client-cert-auth",
				Value:  "true",
				Format: domain.ArgFormatFlagValue,
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--client-cert-auth=true"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsIfTruthyFeature(t *testing.T) {
	env := []string{"PROMETHEUS_ENABLE_NATIVE_HISTOGRAM=true"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				When:   domain.EnvCondition{Name: "PROMETHEUS_ENABLE_NATIVE_HISTOGRAM", Value: "true"},
				Flag:   "--enable-feature",
				Value:  "native-histograms",
				Format: domain.ArgFormatFlagValue,
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--enable-feature=native-histograms"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsCommaSplitList(t *testing.T) {
	env := []string{"THANOS_QUERY_STORE_ADDRESSES=store1:10901,store2:10901"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:    "--endpoint",
				FromEnv: "THANOS_QUERY_STORE_ADDRESSES",
				Split:   ",",
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--endpoint=store1:10901", "--endpoint=store2:10901"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsPatternEnum(t *testing.T) {
	env := []string{
		"THANOS_RECEIVE_LABELS_DATACENTER=dc1",
		"THANOS_RECEIVE_LABELS_TENANT=acme",
		"UNRELATED=x",
	}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:           "--label",
				FromEnvPattern: "^THANOS_RECEIVE_LABELS_(.+)$",
				Value:          `${name}="${value}"`,
				NameTransform:  domain.NameTransformLower,
				Format:         domain.ArgFormatFlagValue,
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{`--label=datacenter="dc1"`, `--label=tenant="acme"`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsWhitespaceSplitRaw(t *testing.T) {
	env := []string{"GRAFANA_MIMIR_EXTRA_ARGS=-log.level=debug -ingester.replication-factor=3"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				FromEnv: "GRAFANA_MIMIR_EXTRA_ARGS",
				Split:   " ",
				Format:  domain.ArgFormatRaw,
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"-log.level=debug", "-ingester.replication-factor=3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsFlagSpace(t *testing.T) {
	env := []string{"THANOS_DATA_DIR=/data"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:    "--data-dir",
				FromEnv: "THANOS_DATA_DIR",
				Format:  domain.ArgFormatFlagSpace,
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--data-dir", "/data"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsNumericGating(t *testing.T) {
	env := []string{"PYROSCOPE_INGESTER_MAX_GLOBAL_SERIES_PER_TENANT=1000"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:    "-ingester.max-global-series-per-tenant",
				FromEnv: "PYROSCOPE_INGESTER_MAX_GLOBAL_SERIES_PER_TENANT",
				ConditionFunc: func(environ []string) bool {
					v := envutil.Get(environ, "PYROSCOPE_INGESTER_MAX_GLOBAL_SERIES_PER_TENANT", "")
					n, err := strconv.Atoi(v)
					return err == nil && n > 0
				},
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"-ingester.max-global-series-per-tenant=1000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsNumericGatingZeroSkips(t *testing.T) {
	env := []string{"PYROSCOPE_INGESTER_MAX_GLOBAL_SERIES_PER_TENANT=0"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:    "-ingester.max-global-series-per-tenant",
				FromEnv: "PYROSCOPE_INGESTER_MAX_GLOBAL_SERIES_PER_TENANT",
				ConditionFunc: func(environ []string) bool {
					v := envutil.Get(environ, "PYROSCOPE_INGESTER_MAX_GLOBAL_SERIES_PER_TENANT", "")
					n, err := strconv.Atoi(v)
					return err == nil && n > 0
				},
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("args = %v, want none", got)
	}
}

func TestBuildArgsArgsFuncOverride(t *testing.T) {
	env := []string{"THANOS_COMPONENT=sidecar"}
	p := domain.Process{
		Arguments: []string{"sidecar"},
		ArgOperations: []domain.ArgOperation{
			{Flag: "--ignored", When: domain.EnvCondition{Name: "THANOS_COMPONENT", Value: "sidecar"}},
		},
		ArgsFunc: func(environ []string) ([]string, error) {
			var args []string
			if envutil.Get(environ, "THANOS_COMPONENT", "") == "sidecar" {
				args = append(args, "--prometheus.url=http://localhost:9090")
			}
			return args, nil
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"sidecar", "--prometheus.url=http://localhost:9090"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsArgsFuncError(t *testing.T) {
	p := domain.Process{
		ArgsFunc: func(environ []string) ([]string, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := buildArgs(p, nil); err == nil {
		t.Fatal("buildArgs should propagate ArgsFunc error")
	}
}

func TestBuildArgsInvalidPattern(t *testing.T) {
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{Flag: "--x", FromEnvPattern: "("},
		},
	}
	if _, err := buildArgs(p, nil); err == nil {
		t.Fatal("buildArgs should error on invalid FromEnvPattern")
	}
}

func TestBuildArgsNameTransformFunc(t *testing.T) {
	env := []string{"THANOS_RECEIVE_LABELS_DATACENTER=dc1"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:           "--label",
				FromEnvPattern: "^THANOS_RECEIVE_LABELS_(.+)$",
				Value:          "${name}=${value}",
				NameTransformFunc: func(name string) string {
					return strings.ToLower(name)
				},
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--label=datacenter=dc1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsValueTransformFunc(t *testing.T) {
	env := []string{"PROMETHEUS_EXTERNAL_URL=http://prom.local"}
	p := domain.Process{
		ArgOperations: []domain.ArgOperation{
			{
				Flag:    "--web.external-url",
				FromEnv: "PROMETHEUS_EXTERNAL_URL",
				ValueTransformFunc: func(v string) string {
					return strings.ToUpper(v)
				},
			},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"--web.external-url=HTTP://PROM.LOCAL"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}

func TestBuildArgsConcatOrder(t *testing.T) {
	env := []string{"FOO=1"}
	p := domain.Process{
		Arguments: []string{"static-arg"},
		ArgOperations: []domain.ArgOperation{
			{Flag: "--foo", FromEnv: "FOO"},
		},
	}
	got, err := buildArgs(p, env)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{"static-arg", "--foo=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
}
