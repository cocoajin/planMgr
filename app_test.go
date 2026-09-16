package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	db, e := openDB(filepath.Join(dir, "tasks.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	loc, _ := time.LoadLocation("Asia/Shanghai")
	return &App{db: db, loc: loc, dir: dir, sessions: map[string]Session{}, passwordSlots: make(chan struct{}, 8)}
}
func TestSchedule(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	cases := []struct {
		p              string
		n, wd, md      int
		at, from, want string
	}{
		{"daily", 1, 1, 1, "02:00", "2026-09-12 03:00:00", "2026-09-13 02:00:00"}, {"days", 3, 1, 1, "02:00", "2026-09-30 03:00:00", "2026-10-01 02:00:00"}, {"hourly", 1, 1, 1, "00:42", "2026-09-12 10:42:00", "2026-09-12 11:42:00"}, {"hours", 3, 1, 1, "00:15", "2026-09-12 10:00:00", "2026-09-12 12:15:00"}, {"minutes", 7, 1, 1, "00:00", "2026-09-12 10:59:00", "2026-09-12 11:00:00"}, {"weekly", 1, 7, 1, "02:00", "2026-09-13 02:00:00", "2026-09-20 02:00:00"}, {"monthly", 1, 1, 31, "02:00", "2026-04-01 03:00:00", "2026-05-31 02:00:00"}, {"monthly", 1, 1, 29, "02:00", "2028-02-01 03:00:00", "2028-02-29 02:00:00"}, {"seconds", 5, 1, 1, "00:00", "2026-09-12 10:03:24", "2026-09-12 10:03:29"}}
	for _, c := range cases {
		t.Run(c.p+c.want, func(t *testing.T) {
			from, _ := time.ParseInLocation("2006-01-02 15:04:05", c.from, loc)
			n, e := nextRun(Task{Period: c.p, N: c.n, At: c.at, Weekday: c.wd, Monthday: c.md, Version: 2}, from.Unix(), loc)
			if e != nil || time.Unix(n, 0).In(loc).Format("2006-01-02 15:04:05") != c.want {
				t.Fatalf("%d %v", n, e)
			}
		})
	}
	n, e := nextRun(Task{Period: "minutes", N: 2, At: "02:00", Version: 1}, 1000, loc)
	if e != nil || n != 1120 {
		t.Fatal("原间隔规则未保留")
	}
	for _, v := range []Input{{"period": "minutes", "interval_n": 0}, {"period": "weekly", "weekday": 8}, {"period": "monthly", "monthday": 32}, {"period": "daily", "at_time": "25:00"}} {
		if _, e := validateSchedule(v); e == nil {
			t.Fatalf("未拒绝 %v", v)
		}
	}
}

type apiClient struct {
	t       *testing.T
	h       http.Handler
	cookies []*http.Cookie
	csrf    string
}

func (c *apiClient) call(action string, body Input) (int, map[string]any) {
	c.t.Helper()
	method := "GET"
	var b bytes.Buffer
	if body != nil {
		method = "POST"
		json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, "http://localhost/api.php?action="+action, &b)
	for _, cookie := range c.cookies {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-CSRF-Token", c.csrf)
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	res := w.Result()
	if cookies := res.Cookies(); len(cookies) > 0 {
		c.cookies = cookies
	}
	var v map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		c.t.Fatalf("invalid response %s", w.Body.String())
	}
	return res.StatusCode, v
}
func (c *apiClient) must(action string, body Input) map[string]any {
	c.t.Helper()
	status, v := c.call(action, body)
	if status != 200 {
		c.t.Fatalf("%s => %d: %v", action, status, v)
	}
	return v
}
func TestAPIAndRunner(t *testing.T) {
	a := testApp(t)
	c := &apiClient{t: t, h: a.handler()}
	if status, _ := c.call("list", nil); status != 401 {
		t.Fatal(status)
	}
	s := c.must("session", nil)
	c.csrf = s["csrf"].(string)
	if s["configured"] != false {
		t.Fatal(s)
	}
	c.must("install", Input{"username": "admin", "password": "cc123", "password_confirm": "cc123"})
	if status, _ := c.call("install", Input{}); status != 422 {
		t.Fatal(status)
	}
	c.must("logout", Input{})
	s = c.must("session", nil)
	c.csrf = s["csrf"].(string)
	if status, _ := c.call("login", Input{"username": "admin", "password": "bad"}); status != 401 {
		t.Fatal(status)
	}
	c.must("login", Input{"username": "admin", "password": "cc123"})
	c.csrf = "bad"
	if status, _ := c.call("clear_logs", Input{}); status != 403 {
		t.Fatal(status)
	}
	c.csrf = c.must("session", nil)["csrf"].(string)
	for _, p := range []string{"daily", "days", "hourly", "hours", "minutes", "weekly", "monthly", "seconds"} {
		v := c.must("preview", Input{"period": p, "interval_n": 3})
		runs := v["runs"].([]any)
		if len(runs) != 3 || runs[0].(float64) >= runs[1].(float64) {
			t.Fatal(v)
		}
	}
	task := Input{"name": "任务", "type": "shell", "script": "printf 'hello go'", "period": "minutes", "interval_n": 3, "timeout": 3}
	id := c.must("save", task)["id"].(float64)
	ids := []any{id}
	task["id"] = id
	task["name"] = "修改任务"
	c.must("save", task)
	c.must("run", Input{"ids": ids})
	if e := a.runDue(context.Background()); e != nil {
		t.Fatal(e)
	}
	logs := c.must(fmt.Sprintf("logs&id=%.0f", id), nil)["logs"].([]any)
	if len(logs) != 1 || !strings.Contains(logs[0].(map[string]any)["output"].(string), "hello go") {
		t.Fatal(logs)
	}
	c.must("toggle", Input{"ids": ids, "enabled": false})
	a.db.Exec("UPDATE tasks SET next_run=0 WHERE id=?", id)
	a.runDue(context.Background())
	var count int
	a.db.QueryRow("SELECT count(*) FROM logs").Scan(&count)
	if count != 1 {
		t.Fatal("暂停仍执行")
	}
	c.must("clear_logs", Input{})
	if len(c.must("list", nil)["tasks"].([]any)) != 1 {
		t.Fatal("清日志删除了任务")
	}
	tx, _ := a.db.Begin()
	for i := 0; i < 501; i++ {
		if _, e := tx.Exec("INSERT INTO logs(task_id,started_at,duration,status,output) VALUES(?,0,0,'success',?)", id, fmt.Sprint(i)); e != nil {
			t.Fatal(e)
		}
	}
	tx.Commit()
	c.must("run", Input{"ids": ids})
	if e := a.runDue(context.Background()); e != nil {
		t.Fatal(e)
	}
	a.db.QueryRow("SELECT count(*) FROM logs").Scan(&count)
	if count != 500 {
		t.Fatal(count)
	}
	c.must("delete", Input{"ids": ids})
	a.db.QueryRow("SELECT count(*) FROM logs").Scan(&count)
	if count != 0 {
		t.Fatal("删除未清日志")
	}
	c.must("logout", Input{})
	if status, _ := c.call("logs&id=1", nil); status != 401 {
		t.Fatal(status)
	}
}
func TestExecution(t *testing.T) {
	a := testApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(500)
		}
		fmt.Fprint(w, r.Method+" OK")
	}))
	defer server.Close()
	for _, method := range []string{"GET", "POST"} {
		status, out, _ := a.execute(context.Background(), Task{Type: "http", URL: server.URL, Method: method, Timeout: 2})
		if status != "success" || !strings.Contains(out, method+" OK") {
			t.Fatal(status, out)
		}
	}
	status, _, _ := a.execute(context.Background(), Task{Type: "http", URL: server.URL + "/fail", Method: "GET", Timeout: 2})
	if status != "failed" {
		t.Fatal(status)
	}
	status, out, d := a.execute(context.Background(), Task{Type: "shell", Script: "sleep 30 & wait", Timeout: 1})
	if status != "failed" || !strings.Contains(out, "124") || d > 4 {
		t.Fatal(status, out, d)
	}
	status, out, _ = a.execute(context.Background(), Task{Type: "shell", Script: "exit 7", Timeout: 2})
	if status != "failed" || !strings.Contains(out, "7") {
		t.Fatal(status, out)
	}
	status, out, d = a.execute(context.Background(), Task{Type: "shell", Script: "sleep 30 & echo done", Timeout: 5})
	if d > 4 || !strings.Contains(out, "done") {
		t.Fatal(status, out, d)
	}
}
func TestImportPHP(t *testing.T) {
	src := t.TempDir()
	db, e := openDB(filepath.Join(src, "data", "tasks.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("cc123"), bcrypt.DefaultCost)
	hash2y := strings.Replace(string(hash), "$2a$", "$2y$", 1)
	os.WriteFile(filepath.Join(src, "config.php"), []byte("<?php return ['username' => 'admin', 'password_hash' => '"+hash2y+"', 'timezone' => 'Asia/Shanghai'];"), 0600)
	db.Exec("INSERT INTO tasks(name,type,period,next_run,created_at) VALUES('old','shell','daily',0,0)")
	db.Close()
	dest := t.TempDir()
	if e = importFromPHP(src, dest); e != nil {
		t.Fatal(e)
	}
	db, e = openDB(filepath.Join(dest, "tasks.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	a := &App{db: db, loc: loc, sessions: map[string]Session{}, passwordSlots: make(chan struct{}, 8)}
	c := &apiClient{t: t, h: a.handler()}
	c.csrf = c.must("session", nil)["csrf"].(string)
	c.must("login", Input{"username": "admin", "password": "cc123"})
	if len(c.must("list", nil)["tasks"].([]any)) != 1 {
		t.Fatal("导入任务丢失")
	}
	if e = importFromPHP(src, dest); e == nil {
		t.Fatal("导入覆盖已有数据库")
	}
}
func TestRuntime(t *testing.T) {
	dir := t.TempDir()
	rt, e := newRuntime(dir, Settings{"127.0.0.1:0", "Asia/Shanghai"}, false)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { rt.cancel(); <-rt.done }()
	if _, e = newRuntime(dir, Settings{"127.0.0.1:0", "Asia/Shanghai"}, false); e == nil {
		t.Fatal("未拒绝重复实例")
	}
	if e = stopRuntime(dir); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, "control.json")); !os.IsNotExist(e) {
		t.Fatal("控制文件未清理")
	}
}

func TestRestartSkipsMissedSchedules(t *testing.T) {
	a := testApp(t)
	now := time.Now().Unix()
	for i, next := range []int64{now - 86400, now + 3600} {
		_, err := a.db.Exec(`INSERT INTO tasks(name,type,period,interval_n,at_time,timeout,enabled,next_run,requested,last_status,created_at,schedule_version) VALUES(?,'shell','seconds',10,'00:00',5,1,?,0,'running',?,2)`, fmt.Sprint(i), next, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := a.skipMissed(now); err != nil {
		t.Fatal(err)
	}
	tasks, err := a.tasks("ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Next != now+10 || tasks[1].Next != now+3600 {
		t.Fatalf("wrong next times: %+v", tasks)
	}
	if err := a.runDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM logs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("missed task executed: %d %v", count, err)
	}
}

func TestHTTPPostBodies(t *testing.T) {
	a := testApp(t)
	type received struct{ method, body, contentType string }
	requests := make(chan received, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		requests <- received{r.Method, string(b), r.Header.Get("Content-Type")}
		w.Write([]byte("ok"))
	}))
	defer server.Close()
	v := Input{"name": "POST 参数测试", "type": "http", "url": server.URL, "method": "POST", "period": "daily", "at_time": "02:00", "timeout": 5}
	for _, c := range []struct{ kind, body, want, ct string }{
		{"form", `[{"key":"名字","value":"a &+中"},{"key":"tag","value":"1"},{"key":"tag","value":"2"}]`, "tag=1&tag=2&%E5%90%8D%E5%AD%97=a+%26%2B%E4%B8%AD", "application/x-www-form-urlencoded"},
		{"json", `{"名字":"a &+中","count":2}`, `{"名字":"a &+中","count":2}`, "application/json"},
		{"none", "", "", ""},
	} {
		v["body_type"], v["body"] = c.kind, c.body
		task, err := validateTask(v)
		if err != nil {
			t.Fatal(err)
		}
		id, err := a.save(task)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := a.tasks("WHERE id=?", id)
		if err != nil || len(saved) != 1 {
			t.Fatal(err)
		}
		if saved[0].BodyType != c.kind || saved[0].Body != c.body {
			t.Fatal("参数保存不一致")
		}
		status, out, _ := a.execute(context.Background(), saved[0])
		if status != "success" {
			t.Fatal(out)
		}
		got := <-requests
		if got.method != "POST" || got.body != c.want || got.contentType != c.ct {
			t.Fatalf("请求不一致: %+v", got)
		}
		v["id"] = id
	}
	v["body_type"], v["body"] = "json", "{bad"
	if _, err := validateTask(v); err == nil {
		t.Fatal("未拒绝无效 JSON")
	}
	v["body_type"], v["body"] = "form", `[{"key":"","value":"a"}]`
	if _, err := validateTask(v); err == nil {
		t.Fatal("未拒绝空参数名")
	}
	v["method"] = "GET"
	task, err := validateTask(v)
	if err != nil || task.Body != "" || task.BodyType != "none" {
		t.Fatal("GET 未清除请求体", err)
	}
}

func TestAllLogs(t *testing.T) {
	a := testApp(t)
	c := &apiClient{t: t, h: a.handler()}
	for _, action := range []string{"all_logs&page=1", "log_detail&id=1"} {
		if status, _ := c.call(action, nil); status != 401 {
			t.Fatal(status)
		}
	}
	c.csrf = c.must("session", nil)["csrf"].(string)
	c.must("install", Input{"username": "admin", "password": "cc123", "password_confirm": "cc123"})
	task, err := validateTask(Input{"name": "日志任务", "type": "shell", "script": "true", "period": "daily"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.save(task)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		if _, err = a.db.Exec("INSERT INTO logs(task_id,started_at,duration,status,output) VALUES(?,?,0.125,'success',?)", id, 1000+i, fmt.Sprint("output", i)); err != nil {
			t.Fatal(err)
		}
	}
	first := c.must("all_logs&page=1", nil)
	logs := first["logs"].([]any)
	if len(logs) != 50 || first["has_more"] != true {
		t.Fatal(first)
	}
	row := logs[0].(map[string]any)
	if row["name"] != "日志任务" || row["started_at"] != float64(1050) || row["output"] != nil {
		t.Fatal(row)
	}
	detail := c.must(fmt.Sprintf("log_detail&id=%.0f", row["id"]), nil)
	if detail["output"] != "output50" {
		t.Fatal(detail)
	}
	second := c.must("all_logs&page=2", nil)
	if len(second["logs"].([]any)) != 1 || second["has_more"] != false {
		t.Fatal(second)
	}
	if status, _ := c.call("all_logs&page=-1", nil); status != 422 {
		t.Fatal(status)
	}
	c.must("clear_logs", Input{})
	if len(c.must("all_logs&page=1", nil)["logs"].([]any)) != 0 {
		t.Fatal("日志未清空")
	}
	if status, _ := c.call(fmt.Sprintf("log_detail&id=%.0f", row["id"]), nil); status != 404 {
		t.Fatal(status)
	}
}
