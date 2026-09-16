package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const columns = "id,name,type,script,url,method,period,interval_n,at_time,timeout,enabled,next_run,requested,last_status,last_run,created_at,weekday,monthday,schedule_version,shell,body_type,body"

func openDB(path string) (*sql.DB, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*sql.DB, error) { db.Close(); return nil, e }
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", `CREATE TABLE IF NOT EXISTS tasks(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL,type TEXT NOT NULL,script TEXT NOT NULL DEFAULT '',url TEXT NOT NULL DEFAULT '',method TEXT NOT NULL DEFAULT 'GET',period TEXT NOT NULL,interval_n INTEGER NOT NULL DEFAULT 1,at_time TEXT NOT NULL DEFAULT '02:00',timeout INTEGER NOT NULL DEFAULT 300,enabled INTEGER NOT NULL DEFAULT 1,next_run INTEGER NOT NULL,requested INTEGER NOT NULL DEFAULT 0,last_status TEXT NOT NULL DEFAULT '',last_run INTEGER,created_at INTEGER NOT NULL)`, `CREATE TABLE IF NOT EXISTS logs(id INTEGER PRIMARY KEY AUTOINCREMENT,task_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE,started_at INTEGER NOT NULL,duration REAL NOT NULL,status TEXT NOT NULL,output TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL)`} {
		if _, e = db.Exec(q); e != nil {
			return fail(e)
		}
	}
	rows, e := db.Query("PRAGMA table_info(tasks)")
	if e != nil {
		return fail(e)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var id, nn, pk int
		var name, kind string
		var def any
		if e = rows.Scan(&id, &name, &kind, &nn, &def, &pk); e != nil {
			rows.Close()
			return fail(e)
		}
		seen[name] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return fail(e)
	}
	for k, v := range map[string]string{"weekday": "INTEGER NOT NULL DEFAULT 1", "monthday": "INTEGER NOT NULL DEFAULT 1", "schedule_version": "INTEGER NOT NULL DEFAULT 1", "shell": "TEXT NOT NULL DEFAULT 'bash'", "body_type": "TEXT NOT NULL DEFAULT 'none'", "body": "TEXT NOT NULL DEFAULT ''"} {
		if !seen[k] {
			if _, e = db.Exec("ALTER TABLE tasks ADD COLUMN " + k + " " + v); e != nil {
				return fail(e)
			}
		}
	}
	_, e = db.Exec("CREATE INDEX IF NOT EXISTS logs_task_id ON logs(task_id,id)")
	if e != nil {
		return fail(e)
	}
	os.Chmod(path, 0600)
	return db, nil
}

type scanner interface{ Scan(...any) error }

func scanTask(r scanner) (Task, error) {
	var t Task
	e := r.Scan(&t.ID, &t.Name, &t.Type, &t.Script, &t.URL, &t.Method, &t.Period, &t.N, &t.At, &t.Timeout, &t.Enabled, &t.Next, &t.Requested, &t.Status, &t.Last, &t.Created, &t.Weekday, &t.Monthday, &t.Version, &t.Shell, &t.BodyType, &t.Body)
	return t, e
}
func (a *App) tasks(query string, args ...any) ([]Task, error) {
	rs, e := a.db.Query("SELECT "+columns+" FROM tasks "+query, args...)
	if e != nil {
		return nil, e
	}
	defer rs.Close()
	items := []Task{}
	for rs.Next() {
		t, e := scanTask(rs)
		if e != nil {
			return nil, e
		}
		items = append(items, t)
	}
	return items, rs.Err()
}
func (a *App) save(t Task) (int64, error) {
	n, e := nextRun(t, time.Now().Unix(), a.loc)
	if e != nil {
		return 0, e
	}
	args := []any{t.Name, t.Type, t.Script, t.URL, t.Method, t.Period, t.N, t.At, t.Timeout, n, t.Weekday, t.Monthday, t.Version, t.Shell, t.BodyType, t.Body}
	if t.ID > 0 {
		r, e := a.db.Exec(`UPDATE tasks SET name=?,type=?,script=?,url=?,method=?,period=?,interval_n=?,at_time=?,timeout=?,next_run=?,weekday=?,monthday=?,schedule_version=?,shell=?,body_type=?,body=? WHERE id=?`, append(args, t.ID)...)
		if e != nil {
			return 0, e
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			return 0, errors.New("任务不存在")
		}
		return t.ID, nil
	}
	r, e := a.db.Exec(`INSERT INTO tasks(name,type,script,url,method,period,interval_n,at_time,timeout,next_run,weekday,monthday,schedule_version,shell,body_type,body,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, append(args, time.Now().Unix())...)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}

type Admin struct {
	Username string `json:"username"`
	Hash     string `json:"password_hash"`
}

func (a *App) admin() (Admin, error) {
	var s string
	e := a.db.QueryRow("SELECT value FROM meta WHERE key='administrator'").Scan(&s)
	if errors.Is(e, sql.ErrNoRows) {
		return Admin{}, nil
	}
	if e != nil {
		return Admin{}, e
	}
	var ad Admin
	e = json.Unmarshal([]byte(s), &ad)
	return ad, e
}
func placeholders(ids []int64) (string, []any) {
	s := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		s[i] = "?"
		args[i] = id
	}
	return strings.Join(s, ","), args
}
func sqlError(e error) error { return fmt.Errorf("数据库操作失败: %w", e) }
