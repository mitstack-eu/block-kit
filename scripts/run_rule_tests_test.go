package scripts_test

// run-rule-tests.sh passes a block whose promtool rule tests pass, fails one whose test fails, whose rule file promtool refuses or whose tests it cannot find, and fetches no promtool for a block holding no rule test.

import (
	"path/filepath"
	"strings"
	"testing"
)

const diskGroup = `name: block-disk
interval: 1m
rules:
  - alert: DiskAlmostFull
    expr: disk_used_ratio > 0.8
    for: 5m
    labels:
      severity: warning
`

func diskTest(usedRatio string) string {
	return `rule_files:
  - metrics/disk.yaml
evaluation_interval: 1m
tests:
  - name: a disk at 90% used fires DiskAlmostFull
    interval: 1m
    input_series:
      - series: 'disk_used_ratio{host="h1"}'
        values: '` + usedRatio + `x10'
    alert_rule_test:
      - eval_time: 10m
        alertname: DiskAlmostFull
        exp_alerts:
          - exp_labels: {severity: warning, host: h1}
`
}

const quietTestOfAMissingGroup = `rule_files:
  - metrics/dsk.yaml
evaluation_interval: 1m
tests:
  - name: a disk at 50% used stays quiet
    interval: 1m
    input_series:
      - series: 'disk_used_ratio{host="h1"}'
        values: '0.5x10'
    alert_rule_test:
      - eval_time: 10m
        alertname: DiskAlmostFull
        exp_alerts: []
`

func TestRunRuleTests(t *testing.T) {
	cases := []struct {
		name   string
		repo   []change
		exit   int
		naming []string
	}{
		{name: "a rule test whose alert fires on its fixture passes", exit: 0,
			repo:   []change{writes("rules/metrics/disk.yaml", diskGroup), writes("testdata/rules/disk.yaml", diskTest("0.9"))},
			naming: []string{"SUCCESS", "passed 1 rule test file"}},
		{name: "a rule test whose alert stays quiet fails, naming the test group", exit: 1,
			repo:   []change{writes("rules/metrics/disk.yaml", diskGroup), writes("testdata/rules/disk.yaml", diskTest("0.7"))},
			naming: []string{"FAILED", "a disk at 90% used fires DiskAlmostFull"}},
		{name: "a rule group holding a field promtool does not know fails, naming the field", exit: 1,
			repo:   []change{writes("rules/metrics/disk.yaml", strings.Replace(diskGroup, "labels:", "lables:", 1)), writes("testdata/rules/disk.yaml", diskTest("0.9"))},
			naming: []string{"lables"}},
		{name: "a quiet rule test naming a group the block does not hold fails, naming the file", exit: 1,
			repo:   []change{writes("rules/metrics/disk.yaml", diskGroup), writes("testdata/rules/disk.yaml", quietTestOfAMissingGroup)},
			naming: []string{"metrics/dsk.yaml", "does not hold"}},
		{name: "a testdata/rules directory holding no yaml test file fails, naming the directory", exit: 1,
			repo:   []change{writes("rules/metrics/disk.yaml", diskGroup), writes("testdata/rules/disk.yml", diskTest("0.9"))},
			naming: []string{"testdata/rules/", "no *.yaml test file"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, _ := gitRepo(t, c.repo...)

			out, exit := runBash(t, nil, "run-rule-tests.sh", repo)

			if exit != c.exit {
				t.Errorf("run-rule-tests.sh exit = %d; want %d\n%s", exit, c.exit, out)
			}
			for _, want := range c.naming {
				if !strings.Contains(out, want) {
					t.Errorf("run-rule-tests.sh output does not name %q:\n%s", want, out)
				}
			}
			if changed := git(t, repo, "status", "--porcelain"); changed != "" {
				t.Errorf("run-rule-tests.sh left the block's tree changed, which blockverify and the image build read next:\n%s", changed)
			}
		})
	}
}

func TestRunRuleTests_ABlockHoldingNoRuleTestPassesWithoutFetchingPromtool(t *testing.T) {
	repo, _ := gitRepo(t, writes("rules/metrics/disk.yaml", diskGroup))
	cache := t.TempDir()

	out, exit := runBash(t, []string{"XDG_CACHE_HOME=" + cache}, "run-rule-tests.sh", repo)

	if exit != 0 || !strings.Contains(out, "holds no rule test") {
		t.Errorf("run-rule-tests.sh exit = %d, output %q; want 0, saying the block holds no rule test", exit, out)
	}
	if fetched, _ := filepath.Glob(filepath.Join(cache, "block-kit", "promtool-*")); len(fetched) > 0 {
		t.Errorf("run-rule-tests.sh fetched %v for a block holding no rule test", fetched)
	}
}
