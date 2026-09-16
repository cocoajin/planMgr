package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type HTTPParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Task struct {
	BodyType  string `json:"body_type"`
	Body      string `json:"body"`
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Script    string `json:"script"`
	URL       string `json:"url"`
	Method    string `json:"method"`
	Period    string `json:"period"`
	N         int    `json:"interval_n"`
	At        string `json:"at_time"`
	Timeout   int    `json:"timeout"`
	Enabled   int    `json:"enabled"`
	Next      int64  `json:"next_run"`
	Requested int    `json:"requested"`
	Status    string `json:"last_status"`
	Last      *int64 `json:"last_run"`
	Created   int64  `json:"created_at"`
	Weekday   int    `json:"weekday"`
	Monthday  int    `json:"monthday"`
	Version   int    `json:"schedule_version"`
	Shell     string `json:"shell"`
}
type Input map[string]any

func (v Input) str(k, def string) string {
	if a, ok := v[k]; ok {
		if s, ok := a.(string); ok {
			return s
		}
		return fmt.Sprint(a)
	}
	return def
}
func (v Input) num(k string, def int) int {
	a, ok := v[k]
	if !ok {
		return def
	}
	n, e := strconv.Atoi(fmt.Sprint(a))
	if e != nil {
		return -1
	}
	return n
}
func (v Input) boolean(k string) bool {
	switch a := v[k].(type) {
	case bool:
		return a
	case json.Number:
		return a != "0"
	case string:
		return a == "1" || a == "true"
	}
	return false
}
func validateSchedule(v Input) (Task, error) {
	t := Task{Period: v.str("period", ""), N: v.num("interval_n", 1), At: v.str("at_time", "02:00"), Weekday: v.num("weekday", 1), Monthday: v.num("monthday", 1), Version: 2}
	max := 1
	switch t.Period {
	case "days":
		max = 31
	case "hours":
		max = 23
	case "minutes", "seconds":
		max = 59
	case "daily", "weekly", "monthly", "hourly":
		t.N = 1
	default:
		return t, errors.New("执行周期无效")
	}
	if t.N < 1 || t.N > max {
		return t, fmt.Errorf("当前周期 N 需为 1–%d", max)
	}
	if t.Weekday < 1 || t.Weekday > 7 || t.Monthday < 1 || t.Monthday > 31 {
		return t, errors.New("星期或日期无效")
	}
	parsed, e := time.Parse("15:04", t.At)
	if e != nil || parsed.Format("15:04") != t.At {
		return t, errors.New("时间格式无效")
	}
	return t, nil
}
func validateTask(v Input) (Task, error) {
	t, e := validateSchedule(v)
	if e != nil {
		return t, e
	}
	t.ID = int64(v.num("id", 0))
	if v.str("id", "") == "" {
		t.ID = 0
	}
	if t.ID < 0 {
		return t, errors.New("任务编号无效")
	}
	t.Name = strings.TrimSpace(v.str("name", ""))
	t.Type = v.str("type", "")
	t.Script = v.str("script", "")
	t.URL = strings.TrimSpace(v.str("url", ""))
	t.Method = v.str("method", "GET")
	t.BodyType = v.str("body_type", "none")
	t.Body = v.str("body", "")
	t.Timeout = v.num("timeout", 300)
	t.Shell = v.str("shell", "auto")
	if t.Name == "" || utf8.RuneCountInString(t.Name) > 100 {
		return t, errors.New("名称需为 1–100 个字符")
	}
	if t.Timeout < 1 || t.Timeout > 3600 {
		return t, errors.New("超时需为 1–3600 秒")
	}
	if t.Method != "GET" && t.Method != "POST" {
		return t, errors.New("只支持 GET/POST")
	}
	switch t.Shell {
	case "auto", "bash":
	default:
		return t, errors.New("命令解释器无效")
	}
	switch t.Type {
	case "shell":
		if strings.TrimSpace(t.Script) == "" || len(t.Script) > 65536 {
			return t, errors.New("请填写脚本（最大 64KB）")
		}
	case "http":
		if t.Method != "POST" {
			t.BodyType = "none"
			t.Body = ""
		}
		if len(t.Body) > 65536 {
			return t, errors.New("请求参数最大 64KB")
		}
		switch t.BodyType {
		case "none":
			t.Body = ""
		case "json":
			if !json.Valid([]byte(t.Body)) {
				return t, errors.New("请输入有效的 JSON")
			}
		case "form":
			var params []HTTPParam
			if err := json.Unmarshal([]byte(t.Body), &params); err != nil {
				return t, errors.New("键值参数格式无效")
			}
			for _, p := range params {
				if strings.TrimSpace(p.Key) == "" {
					return t, errors.New("参数名称不能为空")
				}
			}
		default:
			return t, errors.New("请求参数类型无效")
		}
		u, e := url.Parse(t.URL)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || len(t.URL) > 4096 {
			return t, errors.New("请输入有效的 HTTP/HTTPS 地址")
		}
	default:
		return t, errors.New("任务类型无效")
	}
	return t, nil
}
func nextRun(t Task, from int64, loc *time.Location) (int64, error) {
	if t.N < 1 {
		return 0, errors.New("无效任务周期")
	}
	now := time.Unix(from, 0).In(loc)
	h, m := 0, 0
	if _, e := fmt.Sscanf(t.At, "%d:%d", &h, &m); e != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, errors.New("无效任务时间")
	}
	if t.Version == 1 {
		switch t.Period {
		case "minutes":
			return from + int64(t.N)*60, nil
		case "hours":
			return from + int64(t.N)*3600, nil
		case "hourly":
			return (from/3600 + 1) * 3600, nil
		case "daily", "days":
			n := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)
			if n.Unix() <= from {
				days := 1
				if t.Period == "days" {
					days = t.N
				}
				n = n.AddDate(0, 0, days)
			}
			return n.Unix(), nil
		}
	}
	if t.Period == "seconds" {
		return from + int64(t.N), nil
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 370; i++ {
		d := day.AddDate(0, 0, i)
		wd := int(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		if t.Period == "weekly" && wd != t.Weekday || t.Period == "monthly" && d.Day() != t.Monthday || t.Period == "days" && (d.Day()-1)%t.N != 0 {
			continue
		}
		for hour := 0; hour < 24; hour++ {
			if t.Period == "hours" && hour%t.N != 0 {
				continue
			}
			if t.Period != "hours" && t.Period != "hourly" && t.Period != "minutes" && hour != h {
				continue
			}
			for minute := 0; minute < 60; minute++ {
				if t.Period == "minutes" {
					if minute%t.N != 0 {
						continue
					}
				} else if minute != m {
					continue
				}
				n := time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, loc)
				if n.Hour() != hour || n.Minute() != minute {
					continue
				}
				if n.Unix() > from {
					return n.Unix(), nil
				}
			}
		}
	}
	return 0, errors.New("无法计算下一次执行时间")
}
