package secret

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Synthetic, documentation-style fakes only. None of these values is a real
// credential; they exist purely to exercise the detectors' structure.
const (
	exampleAWSKey     = "AKIAIOSFODNN7EXAMPLE"                       // AWS documentation example access key ID
	exampleGHPToken   = "ghp_EXAMPLE1234567890abcdefghijEXAMPLE"     // synthetic
	exampleGitHubPat  = "github_pat_EXAMPLE1234567890abcdefghij"     // synthetic
	exampleSlackToken = "xoxb-EXAMPLE-1234567890abcdef"              // synthetic
	exampleStripeSK   = "sk-live-EXAMPLE1234567890"                  // synthetic
	exampleStripeRK   = "rk-live-EXAMPLE1234567890"                  // synthetic
	exampleGoogleKey  = "AIzaSyDEXAMPLE1234567890abcdefghijKLMNO"    // synthetic
	exampleSendGrid   = "SG.EXAMPLE0123456789abcdefghij.EXAMPLE0123" // synthetic
)

// buildDiff joins diff lines with newlines, the way git emits them.
func buildDiff(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// fileDiff renders a minimal single-file git diff section.
func fileDiff(path, hunk string, body ...string) string {
	lines := append([]string{
		"diff --git a/" + path + " b/" + path,
		"index 1111111..2222222 100644",
		"--- a/" + path,
		"+++ b/" + path,
		hunk,
	}, body...)
	return buildDiff(lines...)
}

func assertIncidents(t *testing.T, got, want []Incident) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Scan incidents = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("incidents[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func assertUnknown(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Scan unknown = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("unknown[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScanReportsKnownShapes(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		diff  string
		want  []Incident
	}{
		{
			name:  "aws access key id in docs markdown is reported (FU-11: no class filter)",
			paths: []string{"docs/quickstart.md"},
			diff: fileDiff("docs/quickstart.md", "@@ -1,3 +1,4 @@",
				" # Quickstart",
				"-export AWS_SECRET_ACCESS_KEY=placeholder",
				"+key: "+exampleAWSKey,
				" end",
			),
			want: []Incident{{Path: "docs/quickstart.md", Shape: "aws_access_key_id", Line: 2}},
		},
		{
			name:  "github token in generated file is reported",
			paths: []string{"ui/client_gen.go"},
			diff: fileDiff("ui/client_gen.go", "@@ -5,2 +5,3 @@",
				" package ui",
				"+const token = \""+exampleGHPToken+"\"",
			),
			want: []Incident{{Path: "ui/client_gen.go", Shape: "github_token", Line: 6}},
		},
		{
			name:  "slack token is reported",
			paths: []string{"deploy/notify.sh"},
			diff: fileDiff("deploy/notify.sh", "@@ -2,2 +2,3 @@",
				" set -eu",
				"+SLACK_TOKEN=\""+exampleSlackToken+"\"",
			),
			want: []Incident{{Path: "deploy/notify.sh", Shape: "slack_token", Line: 3}},
		},
		{
			name:  "stripe secret key is reported",
			paths: []string{"payments/stripe.go"},
			diff: fileDiff("payments/stripe.go", "@@ -1 +1,2 @@",
				" existing",
				"+STRIPE_KEY=\""+exampleStripeSK+"\"",
			),
			want: []Incident{{Path: "payments/stripe.go", Shape: "stripe_live", Line: 2}},
		},
		{
			name:  "stripe restricted key is reported",
			paths: []string{"payments/refund.go"},
			diff: fileDiff("payments/refund.go", "@@ -1 +1,2 @@",
				" existing",
				"+refund_key = \""+exampleStripeRK+"\"",
			),
			want: []Incident{{Path: "payments/refund.go", Shape: "stripe_live", Line: 2}},
		},
		{
			name:  "google api key is reported",
			paths: []string{"maps/client.go"},
			diff: fileDiff("maps/client.go", "@@ -3,2 +3,3 @@",
				" ctx line",
				"+apikey = \""+exampleGoogleKey+"\"",
			),
			want: []Incident{{Path: "maps/client.go", Shape: "google_api_key", Line: 4}},
		},
		{
			name:  "sendgrid api key is reported",
			paths: []string{"mail/send.go"},
			diff: fileDiff("mail/send.go", "@@ -1,2 +1,3 @@",
				" import (",
				"+\t_ \"sg\" // "+exampleSendGrid,
			),
			want: []Incident{{Path: "mail/send.go", Shape: "sendgrid", Line: 2}},
		},
		{
			name:  "rsa private key block is reported",
			paths: []string{"keys/server.pem"},
			diff: fileDiff("keys/server.pem", "@@ -1,2 +1,3 @@",
				" notes",
				"+-----BEGIN RSA PRIVATE KEY-----",
			),
			want: []Incident{{Path: "keys/server.pem", Shape: "pem_block", Line: 2}},
		},
		{
			name:  "plain private key block is reported",
			paths: []string{"keys/leaf.pem"},
			diff: fileDiff("keys/leaf.pem", "@@ -1,2 +1,3 @@",
				" notes",
				"+-----BEGIN PRIVATE KEY-----",
			),
			want: []Incident{{Path: "keys/leaf.pem", Shape: "pem_block", Line: 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			incidents, unknown := Scan(tt.paths, tt.diff)
			assertIncidents(t, incidents, tt.want)
			assertUnknown(t, unknown, nil)
		})
	}
}

func TestScanDedupesSameShapeToFirstLine(t *testing.T) {
	diff := fileDiff("creds.md", "@@ -1 +1,7 @@",
		" seed",
		"+first "+exampleAWSKey,
		"+second "+exampleAWSKey,
		"+-----BEGIN RSA PRIVATE KEY-----",
		"+-----BEGIN OPENSSH PRIVATE KEY-----",
		"+third "+exampleGHPToken,
		"+fourth "+exampleGitHubPat,
	)
	incidents, unknown := Scan([]string{"creds.md"}, diff)
	// Each shape is reported once at its first line, no matter which of its
	// patterns or how many repeats matched: aws at 2 (not 3), pem at 4 (RSA
	// before OPENSSH), github at 6 (ghp before github_pat).
	assertIncidents(t, incidents, []Incident{
		{Path: "creds.md", Shape: "aws_access_key_id", Line: 2},
		{Path: "creds.md", Shape: "github_token", Line: 6},
		{Path: "creds.md", Shape: "pem_block", Line: 4},
	})
	assertUnknown(t, unknown, nil)
}

func TestScanNumbersLinesAcrossHunks(t *testing.T) {
	diff := fileDiff("app.go", "@@ -12,7 +12,8 @@ func load() {",
		" keep1",
		" keep2",
		"-drop1",
		"-drop2",
		"+key: "+exampleGoogleKey,
		" keep3",
		"@@ -40,3 +41,3 @@ func other() {",
		" ctx",
		"-gone",
		"+export SENDGRID_API_KEY=\""+exampleSendGrid+"\"",
	)
	incidents, unknown := Scan([]string{"app.go"}, diff)
	// Hunk 1 starts at 12: two context lines, two deletions (uncounted), so
	// the addition lands on 14. Hunk 2 restarts the counter at 41.
	assertIncidents(t, incidents, []Incident{
		{Path: "app.go", Shape: "google_api_key", Line: 14},
		{Path: "app.go", Shape: "sendgrid", Line: 42},
	})
	assertUnknown(t, unknown, nil)
}

func TestScanUnparsableHunkHeaderYieldsLineZero(t *testing.T) {
	diff := fileDiff("broken.md", "@@ -x,y +z @@",
		"+key "+exampleAWSKey,
	)
	incidents, unknown := Scan([]string{"broken.md"}, diff)
	assertIncidents(t, incidents, []Incident{
		{Path: "broken.md", Shape: "aws_access_key_id", Line: 0},
	})
	assertUnknown(t, unknown, nil)
}

func TestScanUnquotesQuotedDiffPaths(t *testing.T) {
	diff := buildDiff(
		`diff --git "a/docs/caf\303\251 notes.md" "b/docs/caf\303\251 notes.md"`,
		"index 1111111..2222222 100644",
		`--- "a/docs/caf\303\251 notes.md"`,
		`+++ "b/docs/caf\303\251 notes.md"`,
		"@@ -1 +1,2 @@",
		" hi",
		"+"+exampleAWSKey,
	)
	incidents, unknown := Scan([]string{"docs/café notes.md"}, diff)
	assertIncidents(t, incidents, []Incident{
		{Path: "docs/café notes.md", Shape: "aws_access_key_id", Line: 2},
	})
	assertUnknown(t, unknown, nil)
}

func TestScanIgnoresNoNewlineMarker(t *testing.T) {
	diff := fileDiff("tail.md", "@@ -1,2 +1,2 @@",
		" keep",
		"+"+exampleAWSKey,
		`\ No newline at end of file`,
	)
	incidents, unknown := Scan([]string{"tail.md"}, diff)
	assertIncidents(t, incidents, []Incident{
		{Path: "tail.md", Shape: "aws_access_key_id", Line: 2},
	})
	assertUnknown(t, unknown, nil)
}

func TestScanStableOrderAcrossFilesAndShapes(t *testing.T) {
	diff := fileDiff("zz.md", "@@ -1,2 +1,4 @@",
		" title",
		"-removed",
		"+slack: "+exampleSlackToken,
		"+aws: "+exampleAWSKey,
	) + fileDiff("a_gen.go", "@@ -10 +10 @@",
		"-old token",
		"+const t = \""+exampleGHPToken+"\"",
	) + fileDiff("docs/b.md", "@@ -7,3 +7,4 @@",
		" ctx",
		"+-----BEGIN RSA PRIVATE KEY-----",
		" more",
	)
	incidents, unknown := Scan([]string{"zz.md", "a_gen.go", "docs/b.md"}, diff)
	// Sorted by path, then shape, regardless of diff order.
	assertIncidents(t, incidents, []Incident{
		{Path: "a_gen.go", Shape: "github_token", Line: 10},
		{Path: "docs/b.md", Shape: "pem_block", Line: 8},
		{Path: "zz.md", Shape: "aws_access_key_id", Line: 3},
		{Path: "zz.md", Shape: "slack_token", Line: 2},
	})
	assertUnknown(t, unknown, nil)
}

func TestScanIgnoresNonCredentials(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		diff  string
	}{
		{
			name:  "prose mentioning auth and token",
			paths: []string{"notes.md"},
			diff: fileDiff("notes.md", "@@ -1,2 +1,3 @@",
				" intro",
				"+we rotate the auth token for the auth service",
				" outro",
			),
		},
		{
			name:  "lone base64 blob",
			paths: []string{"blob.txt"},
			diff: fileDiff("blob.txt", "@@ -1 +1,2 @@",
				" old",
				"+ZGF0YSBibG9iIGRhdGEgYmxvYiBkYXRhIGJsb2IgZGF0YQ==",
			),
		},
		{
			name:  "near misses for every shape",
			paths: []string{"near.txt"},
			diff: fileDiff("near.txt", "@@ -1,2 +1,10 @@",
				" old",
				"+ghp_tooshort",
				"+AKIA123",
				"+xoxq-nope",
				"+sk-test-EXAMPLE123456",
				"+AIzaSyDEXAMPLE1234567890abcdefghijKLMN",
				"+SG.short.nope",
				"+-----BEGIN CERTIFICATE-----",
				"+-----BEGIN PUBLIC KEY-----",
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			incidents, unknown := Scan(tt.paths, tt.diff)
			assertIncidents(t, incidents, nil)
			assertUnknown(t, unknown, nil)
		})
	}
}

func TestScanSkipsDeletionOnlyFileSilently(t *testing.T) {
	diff := buildDiff(
		"diff --git a/deleted.txt b/deleted.txt",
		"deleted file mode 100644",
		"index 1111111..0000000",
		"--- a/deleted.txt",
		"+++ /dev/null",
		"@@ -1,2 +0,0 @@",
		"-gone "+exampleAWSKey,
		"-more gone",
	)
	// A pure deletion is neither an incident nor unknown; the removed lines
	// are never scanned. absent.md was never in the diff, so it is unknown.
	incidents, unknown := Scan([]string{"deleted.txt", "absent.md"}, diff)
	assertIncidents(t, incidents, nil)
	assertUnknown(t, unknown, []string{"absent.md"})
}

func TestScanBinaryFileLandsInUnknown(t *testing.T) {
	diff := buildDiff(
		"diff --git a/logo.png b/logo.png",
		"index 1111111..2222222 100644",
		"Binary files a/logo.png and b/logo.png differ",
	)
	incidents, unknown := Scan([]string{"logo.png"}, diff)
	assertIncidents(t, incidents, nil)
	assertUnknown(t, unknown, []string{"logo.png"})
}

func TestScanSelectedFileWithoutHunksIsUnknown(t *testing.T) {
	diff := buildDiff(
		"diff --git a/truncated.md b/truncated.md",
		"index 1111111..2222222 100644",
		"--- a/truncated.md",
		"+++ b/truncated.md",
	)
	incidents, unknown := Scan([]string{"truncated.md"}, diff)
	assertIncidents(t, incidents, nil)
	assertUnknown(t, unknown, []string{"truncated.md"})
}

func TestScanEmptyDiffYieldsUnknownNotClean(t *testing.T) {
	// Empty results are data, never a "clean" verdict: with no diff every
	// input path is uninspectable. Input order is preserved, deduplicated.
	incidents, unknown := Scan([]string{"z.md", "a.md", "z.md"}, "")
	assertIncidents(t, incidents, nil)
	assertUnknown(t, unknown, []string{"z.md", "a.md"})
}

func TestScanOnlyReportsInputPaths(t *testing.T) {
	diff := fileDiff("listed.md", "@@ -1,2 +1,3 @@",
		" intro",
		"+plain added line",
		" outro",
	) + fileDiff("unlisted.txt", "@@ -1,2 +1,3 @@",
		" intro",
		"+key "+exampleAWSKey,
		" outro",
	)
	incidents, unknown := Scan([]string{"listed.md"}, diff)
	assertIncidents(t, incidents, nil)
	assertUnknown(t, unknown, nil)
}

func TestScanModificationWithoutAdditionsIsNotUnknown(t *testing.T) {
	// Context- and deletion-only hunks are still readable text hunks: the
	// file was inspected and simply has no findings.
	diff := fileDiff("ctxonly.md", "@@ -1,3 +1,3 @@",
		" keep",
		"-drop",
		" keep",
	)
	incidents, unknown := Scan([]string{"ctxonly.md"}, diff)
	assertIncidents(t, incidents, nil)
	assertUnknown(t, unknown, nil)
}

func TestIncidentNeverExposesMatchedValue(t *testing.T) {
	typ := reflect.TypeOf(Incident{})
	if typ.NumField() != 3 {
		t.Fatalf("Incident has %d fields, want exactly Path, Shape, Line", typ.NumField())
	}
	fields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		fields[typ.Field(i).Name] = true
	}
	for _, name := range []string{"Path", "Shape", "Line"} {
		if !fields[name] {
			t.Errorf("Incident missing field %s", name)
		}
	}
	if fields["Value"] {
		t.Error("Incident must not have a Value field: findings never carry the matched value")
	}

	diff := fileDiff("leak.md", "@@ -1,3 +1,5 @@",
		" intro",
		"+aws "+exampleAWSKey,
		"+slack "+exampleSlackToken,
		" outro",
	)
	incidents, unknown := Scan([]string{"leak.md"}, diff)
	assertIncidents(t, incidents, []Incident{
		{Path: "leak.md", Shape: "aws_access_key_id", Line: 2},
		{Path: "leak.md", Shape: "slack_token", Line: 3},
	})
	for _, inc := range incidents {
		rendered := fmt.Sprintf("%v %+v %s", inc, inc, inc.String())
		for _, secretValue := range []string{exampleAWSKey, exampleSlackToken} {
			if strings.Contains(rendered, secretValue) {
				t.Errorf("rendered incident %q contains the matched value", rendered)
			}
		}
	}
	for _, path := range unknown {
		for _, secretValue := range []string{exampleAWSKey, exampleSlackToken} {
			if strings.Contains(path, secretValue) {
				t.Errorf("unknown entry %q contains the matched value", path)
			}
		}
	}
}
