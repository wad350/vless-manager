package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpdateHistoryFetchesEveryPageAndIntermediateVersion(t *testing.T) {
	var calls atomic.Int32
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("per_page") != "100" || r.Header.Get("Accept") != "application/vnd.github+json" ||
			r.Header.Get("User-Agent") != "vless-manager/"+Version || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("unexpected history request: %s %v", r.URL, r.Header)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 3 {
			w.Header().Set("Link", fmt.Sprintf(`<%s?per_page=100&page=%d>; rel="next"`, endpoint, page+1))
		}
		batches := map[int][]githubRelease{
			1: {{TagName: "v1.3.0"}, {TagName: "v1.4.0-rc1", Prerelease: true}, {TagName: "v9.0.0", Draft: true}},
			2: {{TagName: "v1.1.0"}, {TagName: "preview", HTMLURL: "javascript:alert(1)"}},
			3: {{TagName: "v1.2.0", Name: "Intermediate", Body: "## Changes\n<script>alert(1)</script>", HTMLURL: "https://github.com/" + UpdateRepository + "/releases/tag/v1.2.0"}, {TagName: "v1.3.0"}},
		}
		_ = json.NewEncoder(w).Encode(batches[page])
	}))
	defer server.Close()
	endpoint = server.URL + "/releases"
	pages := 0
	releases, err := fetchReleaseHistory(context.Background(), server.Client(), endpoint, func(page int) { pages = page })
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || pages != 3 || len(releases) != 5 {
		t.Fatalf("history incomplete: calls=%d pages=%d releases=%+v", calls.Load(), pages, releases)
	}
	for i, version := range []string{"1.3.0", "1.2.0", "1.1.0"} {
		if releases[i].Version != version {
			t.Fatalf("history[%d]=%+v, want %s", i, releases[i], version)
		}
	}
	if releases[1].Name != "Intermediate" || releases[1].Body != "## Changes\n<script>alert(1)</script>" || releases[1].ReleaseURL == "" {
		t.Fatalf("changelog metadata lost: %+v", releases[1])
	}
	if !releases[3].Prerelease || releases[4].Tag != "preview" || releases[4].ReleaseURL != "" {
		t.Fatalf("published extra tags or URL policy lost: %+v", releases[3:])
	}
}

type updateHistoryRoundTripper func(*http.Request) (*http.Response, error)

func (f updateHistoryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateHistoryNeverSucceedsWithTruncatedPages(t *testing.T) {
	for _, scenario := range []string{"http", "invalid-json", "null", "trailing-json", "page-bytes", "total-bytes", "pages", "external-link", "skipped-page", "malformed-link", "too-many-releases"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: updateHistoryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				body, code, link := `[]`, http.StatusOK, ""
				switch scenario {
				case "http":
					code = http.StatusForbidden
				case "invalid-json":
					body = `[{`
				case "null":
					body = `null`
				case "trailing-json":
					body = `[] {}`
				case "page-bytes":
					body = strings.Repeat(" ", maxHistoryPageBytes+1)
				case "total-bytes":
					body = `[{"tag_name":"v1.0.0","body":"` + strings.Repeat("x", 1800<<10) + `"}]`
					link = fmt.Sprintf(`<https://api.github.com/releases?per_page=100&page=%d>; rel="next"`, calls+1)
				case "pages":
					link = fmt.Sprintf(`<https://api.github.com/releases?per_page=100&page=%d>; rel="next"`, calls+1)
				case "external-link":
					link = `<https://evil.example/releases?per_page=100&page=2>; rel="next"`
				case "skipped-page":
					link = `<https://api.github.com/releases?per_page=100&page=3>; rel="next"`
				case "malformed-link":
					link = `broken rel="next"`
				case "too-many-releases":
					batch := make([]githubRelease, 101)
					data, _ := json.Marshal(batch)
					body = string(data)
				}
				return &http.Response{StatusCode: code, Header: http.Header{"Link": []string{link}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			releases, err := fetchReleaseHistory(context.Background(), client, "https://api.github.com/releases", nil)
			if err == nil || releases != nil {
				t.Fatalf("partial history accepted: releases=%+v error=%v", releases, err)
			}
			if scenario == "pages" && calls != maxHistoryPages {
				t.Fatalf("pagination is not bounded: %d requests", calls)
			}
		})
	}
}

func TestUpdateHistoryChecksFullPageWithoutLink(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: updateHistoryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		batch := []githubRelease{}
		if calls == 1 {
			for i := 0; i < 100; i++ {
				batch = append(batch, githubRelease{TagName: fmt.Sprintf("v1.0.%d", i)})
			}
		}
		data, _ := json.Marshal(batch)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}
	releases, err := fetchReleaseHistory(context.Background(), client, "https://api.github.com/releases", nil)
	if err != nil || calls != 2 || len(releases) != 100 {
		t.Fatalf("ambiguous hundred-release page truncated: calls=%d releases=%d err=%v", calls, len(releases), err)
	}
}

func TestUpdateHistoryPreservesCacheAndUpdateStateOnFailure(t *testing.T) {
	u := &appUpdater{status: UpdateStatus{State: "ready", LatestVersion: "1.3.0", Available: true}}
	first := u.finishHistory([]UpdateHistoryRelease{{Tag: "v1.2.0", Body: "Saved notes"}}, "vpn", nil)
	u.history.Busy = true
	failed := u.finishHistory([]UpdateHistoryRelease{{Tag: "partial"}}, "wan", errors.New("page 2 failed"))
	if !failed.Complete || failed.Busy || failed.Error == "" || failed.Transport != "vpn" ||
		len(failed.Releases) != 1 || failed.Releases[0].Tag != "v1.2.0" || !failed.CheckedAt.Equal(*first.CheckedAt) {
		t.Fatalf("complete cache replaced on failure: %+v", failed)
	}
	failed.Releases[0].Body = "caller mutation"
	if got := u.historySnapshot(); got.Releases[0].Body != "Saved notes" || got.CurrentVersion != Version || got.LatestVersion != "1.3.0" {
		t.Fatalf("snapshot alias/version: %+v", got)
	}
	if status := u.snapshot(); status.State != "ready" || !status.Available {
		t.Fatalf("history changed installation state: %+v", status)
	}
}

func TestUpdateHistoryAPIUsesCacheAndExistingAccessControls(t *testing.T) {
	s := &apiServer{cfg: defaultConfig(), mux: http.NewServeMux(), auth: newAuthService(fakeCredentialAuthenticator{})}
	s.updater = &appUpdater{api: s}
	s.updater.finishHistory([]UpdateHistoryRelease{{Tag: "v1.2.0", Body: "notes"}}, "wan", nil)
	s.routes()
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update/history", nil))
		var history UpdateHistoryStatus
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &history) != nil || !history.Complete || len(history.Releases) != 1 {
			t.Fatalf("cached API status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	s.cfg.Settings.AuthEnabled = true
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update/history", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("history bypassed auth: %d", rec.Code)
	}
	s.cfg.Settings.AuthEnabled = false
	request := httptest.NewRequest(http.MethodPost, "http://manager/api/update/history", nil)
	request.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, request)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("history refresh bypassed origin guard: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/update/history", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected history method accepted: %d", rec.Code)
	}
}

func TestUpdateHistoryPaginationAndURLContracts(t *testing.T) {
	base, _ := url.Parse("https://api.github.com/releases")
	if next, err := historyNextPage(`<https://api.github.com/repositories/12/releases?per_page=100&page=2>; rel="next last"`, base, 1); !next || err != nil {
		t.Fatalf("GitHub relation/path contract: next=%v err=%v", next, err)
	}
	for _, value := range []string{"javascript:alert(1)", "https://github.com.evil.example/" + UpdateRepository + "/releases/tag/v1.0.0", "https://user@github.com/" + UpdateRepository + "/releases/tag/v1.0.0", "https://github.com/another/project/releases/tag/v1.0.0"} {
		if historyReleaseURL(value) != "" {
			t.Errorf("unsafe release URL accepted: %s", value)
		}
	}
}

func TestUpdateHistoryUIShowsIntervalAndEscapesChangelog(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to execute the embedded history renderer")
	}
	data, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	var functions strings.Builder
	for _, name := range []string{"esc", "safeGitHubReleaseURL", "compareAppReleaseVersions", "renderAppUpdateHistory", "appUpdateIsBusy", "loadAppUpdateHistory", "rememberAppReleaseNote"} {
		start := regexp.MustCompile(`(?m)^(?:async )?function ` + name + `\(`).FindStringIndex(source)
		if start == nil {
			t.Fatalf("missing history function %s", name)
		}
		rest := source[start[0]:]
		// Search after the declaration line, retaining the actual function body.
		lineEnd := strings.IndexByte(rest, '\n')
		end := regexp.MustCompile(`(?m)^(?:async )?function `).FindStringIndex(rest[lineEnd+1:])
		if end == nil {
			t.Fatalf("missing function boundary for %s", name)
		}
		functions.WriteString(rest[:lineEnd+1+end[0]])
		functions.WriteString("\n")
	}
	script := `const vm=require('vm'),assert=require('assert');
const c={URL,Set,appUpdateStatus:{current_version:'1.0.0',latest_version:'1.3.0'},appUpdateHistoryLoading:false,appUpdateHistoryShowAll:false,appUpdateHistoryOpen:new Set(),settingsActiveSection:'system',renderSettings(){}};
vm.createContext(c);
vm.runInContext(process.env.HISTORY_FUNCTIONS,c);
const items=[{tag:'v1.4.0',version:'1.4.0'},{tag:'v1.3.0',version:'1.3.0',body:'## Fixed\n<img src=x onerror=alert(1)>'},{tag:'v1.2.0',version:'1.2.0',body:'<script>alert(1)</script>',release_url:'javascript:alert(1)'},{tag:'v1.1.0"><svg onload=alert(1)>',name:'<iframe>',version:'1.1.0'},{tag:'v1.0.0',version:'1.0.0'},{tag:'v0.9.0',version:'0.9.0'},{tag:'preview',prerelease:true}];
c.appUpdateHistory={complete:true,releases:items};
let html=c.renderAppUpdateHistory();
assert(html.includes('v1.3.0')&&html.includes('v1.2.0'));
assert(!html.includes('v1.4.0')&&!html.includes('v1.0.0')&&!html.includes('v0.9.0')&&!html.includes('preview'));
assert(html.includes('&lt;img')&&html.includes('&lt;script&gt;')&&html.includes('&lt;svg')&&html.includes('&lt;iframe&gt;'));
assert(!html.includes('<img')&&!html.includes('<script>')&&!html.includes('<svg')&&!html.includes('<iframe')&&!html.includes('href="javascript:'));
c.rememberAppReleaseNote({dataset:{releaseTag:'v1.2.0'},open:true});
html=c.renderAppUpdateHistory();assert(html.includes('data-release-tag="v1.2.0" ontoggle="rememberAppReleaseNote(this)" open'));
c.rememberAppReleaseNote({dataset:{releaseTag:'v1.2.0'},open:false});
html=c.renderAppUpdateHistory();assert(!html.includes('data-release-tag="v1.2.0" ontoggle="rememberAppReleaseNote(this)" open'));
c.appUpdateHistoryShowAll=true;html=c.renderAppUpdateHistory();
assert(html.includes('v1.0.0')&&html.includes('v0.9.0')&&html.includes('preview'));
assert(c.safeGitHubReleaseURL('https://github.com/wad350/vless-manager/releases/tag/v1.2.0'));
assert(!c.safeGitHubReleaseURL('https://github.com.evil.example/a/b/releases/tag/x'));
(async()=>{
 let calls=[];c.api=async(method,path)=>{calls.push(method+' '+path);return {complete:true,releases:items}};
 await c.loadAppUpdateHistory();assert.deepStrictEqual(calls,['GET /update/history']);
 calls=[];await c.loadAppUpdateHistory(true);assert.deepStrictEqual(calls,['GET /update/history','POST /update/history']);
 calls=[];c.api=async(method,path)=>{calls.push(method+' '+path);if(method==='POST')throw Error('rate limit');return {complete:true,releases:items}};
 await c.loadAppUpdateHistory(true);assert.strictEqual(c.appUpdateHistory.releases.length,items.length);assert.strictEqual(c.appUpdateHistory.error,'rate limit');assert.strictEqual(c.appUpdateHistoryLoading,false);
})().catch(e=>{console.error(e);process.exitCode=1});`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", script)
	command.Env = append(command.Environ(), "HISTORY_FUNCTIONS="+functions.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("history renderer: %v\n%s", err, output)
	}
}
