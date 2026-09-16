package main

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Session struct {
	CSRF   string
	Auth   string
	Expiry time.Time
}
type App struct {
	db            *sql.DB
	loc           *time.Location
	dir           string
	mu            sync.Mutex
	sessions      map[string]Session
	passwordSlots chan struct{}
	runMu         sync.Mutex
}

func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, s string) { send(w, status, map[string]any{"error": s}) }
func (a *App) dbFail(w http.ResponseWriter, e error) {
	log.Print(e)
	fail(w, 500, "数据库操作失败，请检查服务日志")
}
func (a *App) session(w http.ResponseWriter, r *http.Request) (string, Session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	id := ""
	if c, e := r.Cookie("PLANMGR_GO"); e == nil {
		id = c.Value
	}
	s, ok := a.sessions[id]
	if !ok || s.Expiry.Before(now) {
		for k, s := range a.sessions {
			if s.Expiry.Before(now) {
				delete(a.sessions, k)
			}
		}
		if len(a.sessions) >= 5000 {
			for k := range a.sessions {
				delete(a.sessions, k)
				break
			}
		}
		id = randomToken()
		s = Session{CSRF: randomToken(), Expiry: now.Add(24 * time.Hour)}
		a.sessions[id] = s
		http.SetCookie(w, &http.Cookie{Name: "PLANMGR_GO", Value: id, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	}
	return id, s
}
func (a *App) authenticate(w http.ResponseWriter, r *http.Request, id string, s Session, hash string) {
	a.mu.Lock()
	delete(a.sessions, id)
	id = randomToken()
	s.Auth = hash
	s.Expiry = time.Now().Add(24 * time.Hour)
	a.sessions[id] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "PLANMGR_GO", Value: id, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	send(w, 200, map[string]any{"ok": true})
}
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", a.api)
	mux.HandleFunc("/api", a.api)
	assets, _ := fs.Sub(web, "web")
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" && r.URL.Path != "/index.php" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/index.php" {
			r.URL.Path = "/"
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	ad, e := a.admin()
	if e != nil {
		a.dbFail(w, e)
		return
	}
	id, s := a.session(w, r)
	action := r.URL.Query().Get("action")
	if action == "" {
		action = "list"
	}
	auth := s.Auth != "" && s.Auth == ad.Hash
	if action == "session" {
		send(w, 200, map[string]any{"authenticated": auth, "csrf": s.CSRF, "configured": ad.Hash != "", "platform": runtime.GOOS})
		return
	}
	v := Input{}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		if e = dec.Decode(&v); e != nil || v == nil {
			fail(w, 422, "请求格式错误")
			return
		}
		if subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			fail(w, 403, "页面已过期，请刷新")
			return
		}
	} else if r.Method != http.MethodGet || (action != "list" && action != "logs" && action != "all_logs" && action != "log_detail") {
		fail(w, 405, "需要 POST 请求")
		return
	}
	if action == "install" || action == "login" {
		select {
		case a.passwordSlots <- struct{}{}:
			defer func() { <-a.passwordSlots }()
		default:
			fail(w, 429, "请求过多，请稍后再试")
			return
		}
		user := strings.TrimSpace(v.str("username", ""))
		pwd := v.str("password", "")
		if action == "install" {
			if ad.Hash != "" {
				fail(w, 422, "已完成初始化，请登录")
				return
			}
			if !regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`).MatchString(user) {
				fail(w, 422, "用户名需为 3–32 位字母、数字、下划线、点或横线")
				return
			}
			if len(pwd) < 5 || len(pwd) > 72 {
				fail(w, 422, "密码需为 5–72 字节")
				return
			}
			if pwd != v.str("password_confirm", "") {
				fail(w, 422, "两次输入的密码不一致")
				return
			}
			hash, e := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
			if e != nil {
				a.dbFail(w, e)
				return
			}
			ad = Admin{user, string(hash)}
			b, _ := json.Marshal(ad)
			res, e := a.db.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES('administrator',?)", string(b))
			if e != nil {
				a.dbFail(w, e)
				return
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				fail(w, 422, "已完成初始化，请登录")
				return
			}
		} else {
			time.Sleep(300 * time.Millisecond)
			hash := strings.Replace(ad.Hash, "$2y$", "$2a$", 1)
			if user != ad.Username || bcrypt.CompareHashAndPassword([]byte(hash), []byte(pwd)) != nil {
				fail(w, 401, "用户名或密码错误")
				return
			}
		}
		a.authenticate(w, r, id, s, ad.Hash)
		return
	}
	if !auth {
		fail(w, 401, "请先登录")
		return
	}
	switch action {
	case "logout":
		a.mu.Lock()
		delete(a.sessions, id)
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "PLANMGR_GO", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		send(w, 200, map[string]bool{"ok": true})
	case "list":
		tasks, e := a.tasks("ORDER BY id DESC")
		if e != nil {
			a.dbFail(w, e)
			return
		}
		var heartbeat string
		e = a.db.QueryRow("SELECT value FROM meta WHERE key='heartbeat'").Scan(&heartbeat)
		if e != nil && e != sql.ErrNoRows {
			a.dbFail(w, e)
			return
		}
		send(w, 200, map[string]any{"tasks": tasks, "heartbeat": heartbeat, "timezone": a.loc.String(), "platform": runtime.GOOS})
	case "all_logs":
		page := Input{"page": r.URL.Query().Get("page")}.num("page", 1)
		if page < 1 || page > 1000000 {
			fail(w, 422, "页码无效")
			return
		}
		rs, e := a.db.Query(`SELECT l.id,t.name,l.started_at,l.duration,l.status FROM logs l JOIN tasks t ON t.id=l.task_id ORDER BY l.id DESC LIMIT 51 OFFSET ?`, (page-1)*50)
		if e != nil {
			a.dbFail(w, e)
			return
		}
		defer rs.Close()
		items := []map[string]any{}
		for rs.Next() {
			var id, started int64
			var name, status string
			var duration float64
			if e = rs.Scan(&id, &name, &started, &duration, &status); e != nil {
				a.dbFail(w, e)
				return
			}
			items = append(items, map[string]any{"id": id, "name": name, "started_at": started, "duration": duration, "status": status})
		}
		if e = rs.Err(); e != nil {
			a.dbFail(w, e)
			return
		}
		more := len(items) > 50
		if more {
			items = items[:50]
		}
		send(w, 200, map[string]any{"logs": items, "page": page, "has_more": more})
	case "log_detail":
		var name, status, output string
		var started int64
		var duration float64
		e := a.db.QueryRow(`SELECT t.name,l.started_at,l.duration,l.status,l.output FROM logs l JOIN tasks t ON t.id=l.task_id WHERE l.id=?`, r.URL.Query().Get("id")).Scan(&name, &started, &duration, &status, &output)
		if e == sql.ErrNoRows {
			fail(w, 404, "日志已删除或已被新记录替换")
			return
		}
		if e != nil {
			a.dbFail(w, e)
			return
		}
		send(w, 200, map[string]any{"name": name, "started_at": started, "duration": duration, "status": status, "output": output})
	case "logs":
		rs, e := a.db.Query("SELECT id,task_id,started_at,duration,status,output FROM logs WHERE task_id=? ORDER BY id DESC LIMIT 500", r.URL.Query().Get("id"))
		if e != nil {
			a.dbFail(w, e)
			return
		}
		defer rs.Close()
		items := []map[string]any{}
		for rs.Next() {
			var id, tid, started int64
			var duration float64
			var status, output string
			if e = rs.Scan(&id, &tid, &started, &duration, &status, &output); e != nil {
				a.dbFail(w, e)
				return
			}
			items = append(items, map[string]any{"id": id, "task_id": tid, "started_at": started, "duration": duration, "status": status, "output": output})
		}
		if e = rs.Err(); e != nil {
			a.dbFail(w, e)
			return
		}
		send(w, 200, map[string]any{"logs": items})
	case "clear_logs":
		res, e := a.db.Exec("DELETE FROM logs")
		if e != nil {
			a.dbFail(w, e)
			return
		}
		n, _ := res.RowsAffected()
		send(w, 200, map[string]any{"ok": true, "deleted": n})
	case "preview":
		t, e := validateSchedule(v)
		if e != nil {
			fail(w, 422, e.Error())
			return
		}
		now := time.Now().Unix()
		from := now
		runs := []int64{}
		for i := 0; i < 3; i++ {
			from, e = nextRun(t, from, a.loc)
			if e != nil {
				fail(w, 422, e.Error())
				return
			}
			runs = append(runs, from)
		}
		send(w, 200, map[string]any{"schedule": t, "server_time": now, "timezone": a.loc.String(), "runs": runs})
	case "save":
		t, e := validateTask(v)
		if e != nil {
			fail(w, 422, e.Error())
			return
		}
		id, e := a.save(t)
		if e != nil {
			a.dbFail(w, e)
			return
		}
		send(w, 200, map[string]any{"id": id})
	case "delete", "toggle", "run":
		raw, ok := v["ids"].([]any)
		if !ok || len(raw) < 1 || len(raw) > 500 {
			fail(w, 422, "请选择 1–500 个任务")
			return
		}
		ids := []int64{}
		for _, x := range raw {
			n := Input{"id": x}.num("id", 0)
			if n < 1 {
				fail(w, 422, "任务编号无效")
				return
			}
			ids = append(ids, int64(n))
		}
		in, args := placeholders(ids)
		if action == "delete" {
			_, e = a.db.Exec("DELETE FROM tasks WHERE id IN ("+in+")", args...)
		}
		if action == "run" {
			_, e = a.db.Exec("UPDATE tasks SET requested=1 WHERE id IN ("+in+") AND last_status!='running'", args...)
		}
		if action == "toggle" {
			e = a.toggle(ids, v.boolean("enabled"))
		}
		if e != nil {
			a.dbFail(w, e)
			return
		}
		send(w, 200, map[string]bool{"ok": true})
	default:
		fail(w, 404, fmt.Sprintf("未知操作: %s", action))
	}
}
func (a *App) toggle(ids []int64, on bool) error {
	in, args := placeholders(ids)
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rs, e := tx.Query("SELECT "+columns+" FROM tasks WHERE id IN ("+in+")", args...)
	if e != nil {
		return e
	}
	list := []Task{}
	for rs.Next() {
		t, e := scanTask(rs)
		if e != nil {
			rs.Close()
			return e
		}
		list = append(list, t)
	}
	e = rs.Err()
	rs.Close()
	if e != nil {
		return e
	}
	enabled := 0
	if on {
		enabled = 1
	}
	for _, t := range list {
		if on {
			t.Next, e = nextRun(t, time.Now().Unix(), a.loc)
			if e != nil {
				return e
			}
		}
		if _, e = tx.Exec("UPDATE tasks SET enabled=?,next_run=? WHERE id=?", enabled, t.Next, t.ID); e != nil {
			return e
		}
	}
	return tx.Commit()
}
