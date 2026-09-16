package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type cappedWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	left := 65536 - c.b.Len()
	if left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		c.b.Write(p)
	}
	return n, nil
}
func (a *App) execute(ctx context.Context, t Task) (string, string, float64) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(t.Timeout)*time.Second)
	defer cancel()
	out := &cappedWriter{}
	status := "failed"
	prefix := ""
	if t.Type == "http" {
		body, contentType := "", ""
		if t.Method == "POST" {
			switch t.BodyType {
			case "json":
				body, contentType = t.Body, "application/json"
			case "form":
				var params []HTTPParam
				if err := json.Unmarshal([]byte(t.Body), &params); err != nil {
					return "failed", "键值参数格式无效", 0
				}
				values := url.Values{}
				for _, p := range params {
					values.Add(p.Key, p.Value)
				}
				body, contentType = values.Encode(), "application/x-www-form-urlencoded"
			}
		}
		req, e := http.NewRequestWithContext(ctx, t.Method, t.URL, strings.NewReader(body))
		if e != nil {
			return "failed", e.Error(), 0
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: time.Duration(t.Timeout) * time.Second}
		resp, e := client.Do(req)
		if e != nil {
			prefix = "HTTP 0 " + e.Error()
		} else {
			_, err := io.Copy(out, resp.Body)
			resp.Body.Close()
			prefix = fmt.Sprintf("HTTP %d", resp.StatusCode)
			if err != nil {
				prefix += " " + err.Error()
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				status = "success"
			}
		}
	} else {
		file, e := os.CreateTemp("", "planmgr-*.sh")
		if e != nil {
			return "failed", e.Error(), 0
		}
		path := file.Name()
		defer os.Remove(path)
		script := t.Script
		if _, e = file.WriteString(script); e != nil {
			file.Close()
			return "failed", e.Error(), 0
		}
		file.Close()
		cmd := exec.Command("/bin/bash", path)
		cmd.Dir = filepath.Clean(a.dir)
		cmd.Stdout = out
		cmd.Stderr = out
		cmd.WaitDelay = 2 * time.Second
		e = runProcess(ctx, cmd)
		prefix = "Exit 0"
		if e == nil {
			status = "success"
		} else {
			prefix = "Exit -1 " + e.Error()
			if x, ok := e.(*exec.ExitError); ok {
				prefix = fmt.Sprintf("Exit %d", x.ExitCode())
			}
			if ctx.Err() != nil {
				prefix = "Exit 124（执行超时或服务停止）"
			}
		}
	}
	return status, prefix + "\n" + strings.ToValidUTF8(out.b.String(), "�"), time.Since(started).Seconds()
}
func (a *App) runDue(ctx context.Context) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	tasks, e := a.tasks("WHERE (enabled=1 AND next_run<=?) OR requested=1 ORDER BY next_run", time.Now().Unix())
	if e != nil {
		return e
	}
	for _, candidate := range tasks {
		if ctx.Err() != nil {
			break
		}
		tx, e := a.db.Begin()
		if e != nil {
			return e
		}
		t, e := scanTask(tx.QueryRow("SELECT "+columns+" FROM tasks WHERE id=? AND ((enabled=1 AND next_run<=?) OR requested=1)", candidate.ID, time.Now().Unix()))
		if e == sql.ErrNoRows {
			tx.Rollback()
			continue
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		start := time.Now().Unix()
		if t.Next <= start {
			t.Next, e = nextRun(t, start, a.loc)
			if e != nil {
				tx.Rollback()
				return e
			}
		}
		_, e = tx.Exec("UPDATE tasks SET requested=0,last_status='running',last_run=?,next_run=? WHERE id=?", start, t.Next, t.ID)
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		status, out, duration := a.execute(ctx, t)
		tx, e = a.db.Begin()
		if e != nil {
			return e
		}
		_, e = tx.Exec("INSERT INTO logs(task_id,started_at,duration,status,output) SELECT id,?,?,?,? FROM tasks WHERE id=?", start, duration, status, out, t.ID)
		if e == nil {
			_, e = tx.Exec("UPDATE tasks SET last_status=? WHERE id=?", status, t.ID)
		}
		if e == nil {
			_, e = tx.Exec("DELETE FROM logs WHERE task_id=? AND id NOT IN (SELECT id FROM logs WHERE task_id=? ORDER BY id DESC LIMIT 500)", t.ID, t.ID)
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}

// Skip schedules missed while the service was stopped; keep explicit manual requests.
func (a *App) skipMissed(now int64) error {
	tasks, err := a.tasks("WHERE next_run<=?", now)
	if err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range tasks {
		next, err := nextRun(t, now, a.loc)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE tasks SET next_run=? WHERE id=?", next, t.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE tasks SET last_status='interrupted' WHERE last_status='running'"); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) scheduler(ctx context.Context) {
	ready := false
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !ready {
			if e := a.skipMissed(time.Now().Unix()); e != nil {
				log.Print("初始化执行周期: ", e)
			} else {
				ready = true
			}
		}
		if ready {
			if e := a.runDue(ctx); e != nil {
				log.Print("任务执行器: ", e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) heartbeat(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if _, e := a.db.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('heartbeat',?)", time.Now().Unix()); e != nil {
			log.Print(e)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
