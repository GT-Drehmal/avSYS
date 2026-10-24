package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Who is on which server, said by each tab, once each, and gone as they
// leave, close the tab, or go quiet.
func TestPresence(t *testing.T) {
	p := newPanel(t)
	put := func(who, body string) {
		t.Helper()
		if code, _ := p.do(who, "PUT", "/orgs/alpha/presence", body); code != http.StatusNoContent {
			t.Fatalf("%s: %d", body, code)
		}
	}
	here := func() []Present {
		t.Helper()
		return p.srv.presenceOf(map[string]string{"mc-alpha": "alpha"})[0].People
	}
	where := func() []string {
		t.Helper()
		var out []string
		for _, x := range here() {
			out = append(out, x.User.ID+" "+x.Server+" "+x.File)
		}
		return out
	}
	op, viewer := p.users["operator"].ID, p.users["viewer"].ID

	closeOp := p.srv.streamOpened(p.users["operator"], "operator-tab-1")
	put("operator", `{"tab":"operator-tab-1","server":"survival","file":"/server.properties"}`)
	// The same page in another tab is the same.
	put("operator", `{"tab":"operator-tab-2","server":"survival","file":"/server.properties"}`)
	// A viewer cannot open files: one said is not kept.
	put("viewer", `{"tab":"viewer-tab-1","server":"survival","file":"/server.properties"}`)
	want := []string{op + " survival /server.properties", viewer + " survival "}
	if p.users["viewer"].Username < p.users["operator"].Username {
		want = []string{want[1], want[0]}
	}
	if got := where(); !reflect.DeepEqual(got, want) {
		t.Fatalf("here: %q, want %q", got, want)
	}

	// Leaving the servers, and closing the tab, takes each away.
	put("viewer", `{"tab":"viewer-tab-1","server":""}`)
	put("operator", `{"tab":"operator-tab-2","server":""}`)
	if got := where(); !reflect.DeepEqual(got, []string{op + " survival /server.properties"}) {
		t.Fatalf("after leaving: %q", got)
	}
	closeOp()
	if got := where(); len(got) != 0 {
		t.Fatalf("after its tab closed: %q", got)
	}

	// One not heard from in a while is gone; one with its stream open
	// stays known, on none of them.
	put("operator", `{"tab":"operator-tab-3","server":"survival"}`)
	defer p.srv.streamOpened(p.users["viewer"], "viewer-tab-2")()
	put("viewer", `{"tab":"viewer-tab-2","server":"creative"}`)
	p.srv.forgetQuiet(time.Now().Add(presenceFor + time.Second))
	if got := where(); len(got) != 0 {
		t.Fatalf("after going quiet: %q", got)
	}
	p.srv.presence.mu.Lock()
	_, kept := p.srv.presence.tabs[p.users["viewer"].ID+"/viewer-tab-2"]
	p.srv.presence.mu.Unlock()
	if !kept {
		t.Error("a tab with its stream open was forgotten")
	}
}

func TestPresenceIsChecked(t *testing.T) {
	p := newPanel(t)
	for _, body := range []string{
		`{"tab":"short","server":"survival"}`,
		`{"tab":"a-good-tab","server":"Not A Name"}`,
		`{"tab":"a-good-tab","server":"","file":"/a.txt"}`,
		`{"tab":"a-good-tab","server":"survival","file":"` + strings.Repeat("a", 1025) + `"}`,
	} {
		if code, _ := p.do("owner", "PUT", "/orgs/alpha/presence", body); code != http.StatusBadRequest {
			t.Errorf("%.80s: %d, want 400", body, code)
		}
	}
}

// What files others have open is told only to those who can open them.
func TestViewersAreNotToldFiles(t *testing.T) {
	p := newPanel(t)
	ts := httptest.NewServer(p.h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := func(who string) *bufio.Scanner {
		req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events?tab="+who+"-stream", nil)
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return bufio.NewScanner(res.Body)
	}
	viewer, operator := stream("viewer"), stream("operator")
	for {
		p.srv.Events.mu.Lock()
		n := len(p.srv.Events.subs)
		p.srv.Events.mu.Unlock()
		if n == 2 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("the streams never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, _ := p.do("operator", "PUT", "/orgs/alpha/presence", `{"tab":"operator-stream","server":"survival","file":"/ops.json"}`); code != http.StatusNoContent {
		t.Fatalf("presence: %d", code)
	}
	// The last presence each is told.
	told := func(s *bufio.Scanner) string {
		t.Helper()
		for s.Scan() {
			if data, ok := strings.CutPrefix(s.Text(), "data: "); ok && strings.Contains(data, `"survival"`) {
				return data
			}
		}
		t.Fatalf("never told: %v", s.Err())
		return ""
	}
	if got := told(operator); !strings.Contains(got, `"file":"/ops.json"`) {
		t.Errorf("the operator was told %s", got)
	}
	if got := told(viewer); strings.Contains(got, "ops.json") {
		t.Errorf("the viewer was told %s", got)
	}
}

// Told as it changes, from many tabs at once, what is told last is what is
// so: none told after another that was newer.
func TestPresenceToldInOrder(t *testing.T) {
	p := newPanel(t)
	ch := p.srv.Events.subscribe()
	defer p.srv.Events.unsubscribe(ch)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tab := fmt.Sprintf(`"tab":"operator-tab-%02d"`, i)
			// Each with a file of its own, so each is listed.
			p.do("operator", "PUT", "/orgs/alpha/presence", fmt.Sprintf(`{%s,"server":"survival","file":"/f-%02d"}`, tab, i))
			if i%2 == 0 {
				p.do("operator", "PUT", "/orgs/alpha/presence", `{`+tab+`,"server":""}`)
			}
		}()
	}
	wg.Wait()
	var last *presenceView
	for {
		select {
		case e := <-ch:
			if v, ok := e.Data.(presenceView); ok {
				last = &v
			}
			continue
		default:
		}
		break
	}
	if last == nil {
		t.Fatal("nothing was told")
	}
	if now := p.srv.presenceOf(map[string]string{"mc-alpha": "alpha"})[0]; !reflect.DeepEqual(*last, now) {
		t.Errorf("told last %+v, but now it is %+v", *last, now)
	}
}

// Asked for, it is who is there now; to a viewer, without files.
func TestGetPresence(t *testing.T) {
	p := newPanel(t)
	p.do("operator", "PUT", "/orgs/alpha/presence", `{"tab":"operator-tab","server":"survival","file":"/ops.json"}`)
	for who, file := range map[string]string{"operator": "/ops.json", "viewer": ""} {
		req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/presence", nil)
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var people []Present
		if err := json.Unmarshal(rec.Body.Bytes(), &people); err != nil || len(people) != 1 {
			t.Fatalf("%s: %d %s", who, rec.Code, rec.Body)
		}
		if people[0].Server != "survival" || people[0].File != file {
			t.Errorf("%s was told %+v", who, people[0])
		}
	}
}
