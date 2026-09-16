package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/gofrs/flock"
	"github.com/kardianos/service"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

//go:embed web/*
var web embed.FS

const version = "1.0.0"

type Settings struct {
	Addr     string `json:"addr"`
	Timezone string `json:"timezone"`
}
type Runtime struct {
	app      *App
	server   *http.Server
	listener net.Listener
	lock     *flock.Flock
	cancel   context.CancelFunc
	done     chan struct{}
	logfile  *os.File
	control  string
}
type Control struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func newRuntime(dir string, cfg Settings, noRunner bool) (*Runtime, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	dir, _ = filepath.Abs(dir)
	loc, e := time.LoadLocation(cfg.Timezone)
	if e != nil {
		return nil, e
	}
	lock := flock.New(filepath.Join(dir, "tasks.sqlite.service.lock"))
	ok, e := lock.TryLock()
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, errors.New("同一数据目录已被其他实例使用（请先停止 PHP/Go 执行器）")
	}
	listener, e := net.Listen("tcp", cfg.Addr)
	if e != nil {
		lock.Close()
		return nil, e
	}
	db, e := openDB(filepath.Join(dir, "tasks.sqlite"))
	if e != nil {
		listener.Close()
		lock.Close()
		return nil, e
	}
	logs := filepath.Join(dir, "logs")
	if e = os.MkdirAll(logs, 0700); e != nil {
		db.Close()
		listener.Close()
		lock.Close()
		return nil, e
	}
	logfile, e := os.OpenFile(filepath.Join(logs, "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		db.Close()
		listener.Close()
		lock.Close()
		return nil, e
	}
	log.SetOutput(io.MultiWriter(os.Stderr, logfile))
	a := &App{db: db, loc: loc, dir: filepath.Dir(dir), sessions: map[string]Session{}, passwordSlots: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	rt := &Runtime{app: a, listener: listener, lock: lock, cancel: cancel, done: make(chan struct{}), logfile: logfile, control: filepath.Join(dir, "control.json")}
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	control := Control{"http://" + net.JoinHostPort(host, port), randomToken()}
	b, _ := json.Marshal(control)
	if e = os.WriteFile(rt.control, b, 0600); e != nil {
		cancel()
		db.Close()
		listener.Close()
		lock.Close()
		logfile.Close()
		return nil, e
	}
	handler := a.handler()
	rt.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_control/stop" {
			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if r.Method != "POST" || !net.ParseIP(ip).IsLoopback() || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Control-Token")), []byte(control.Token)) != 1 {
				http.NotFound(w, r)
				return
			}
			send(w, 200, map[string]bool{"ok": true})
			cancel()
			return
		}
		handler.ServeHTTP(w, r)
	})}
	go func() {
		defer close(rt.done)
		var wg sync.WaitGroup
		if !noRunner {
			wg.Add(2)
			go func() { defer wg.Done(); a.scheduler(ctx) }()
			go func() { defer wg.Done(); a.heartbeat(ctx) }()
		}
		go func() {
			if e := rt.server.Serve(listener); e != nil && e != http.ErrServerClosed {
				log.Print(e)
			}
			cancel()
		}()
		<-ctx.Done()
		shutdown, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		rt.server.Shutdown(shutdown)
		wg.Wait()
		db.Close()
		os.Remove(rt.control)
		lock.Close()
		log.SetOutput(os.Stderr)
		logfile.Close()
	}()
	log.Printf("PlanMgr %s 已启动 %s，数据目录 %s", version, control.URL, dir)
	return rt, nil
}
func stopRuntime(dir string) error {
	b, e := os.ReadFile(filepath.Join(dir, "control.json"))
	if e != nil {
		return e
	}
	var c Control
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	req, e := http.NewRequest("POST", c.URL+"/_control/stop", nil)
	if e != nil {
		return e
	}
	req.Header.Set("X-Control-Token", c.Token)
	client := http.Client{Timeout: 5 * time.Second}
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("停止失败: %d", res.StatusCode)
	}
	for i := 0; i < 100; i++ {
		if _, e = os.Stat(filepath.Join(dir, "control.json")); os.IsNotExist(e) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("服务仍在退出，请稍后检查")
}

type daemon struct {
	dir string
	cfg Settings
	rt  *Runtime
}

func (d *daemon) Start(service.Service) error {
	var e error
	d.rt, e = newRuntime(d.dir, d.cfg, false)
	return e
}
func (d *daemon) Stop(service.Service) error {
	if d.rt != nil {
		d.rt.cancel()
		<-d.rt.done
	}
	return nil
}
func main() {
	exe, e := os.Executable()
	if e != nil {
		log.Fatal(e)
	}
	defaultDir := filepath.Join(filepath.Dir(exe), "data")
	dir := flag.String("data-dir", defaultDir, "SQLite 和运行日志目录")
	addr := flag.String("addr", "", "监听地址，默认 127.0.0.1:8600")
	zone := flag.String("timezone", "", "默认 Asia/Shanghai")
	stop := flag.Bool("stop", false, "停止此数据目录对应的实例")
	serviceAction := flag.String("service", "", "install/start/stop/uninstall/status")
	importPHP := flag.String("import-php", "", "从 PHP 工程目录导入数据库和旧配置账号，仅在新目录操作")
	noRunner := flag.Bool("web-only", false, "只启动网页，便于还原后先检查任务")
	ver := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *ver {
		fmt.Println(version)
		return
	}
	*dir, e = filepath.Abs(*dir)
	if e != nil {
		log.Fatal(e)
	}
	if *stop {
		if e = stopRuntime(*dir); e != nil {
			log.Fatal(e)
		}
		return
	}
	if *importPHP != "" {
		if e = importFromPHP(*importPHP, *dir); e != nil {
			log.Fatal(e)
		}
		fmt.Println("导入完成；尚未启动任务，请先用 --web-only 检查。")
		return
	}
	cfg := Settings{Addr: "127.0.0.1:8600", Timezone: "Asia/Shanghai"}
	b, e := os.ReadFile(filepath.Join(*dir, "settings.json"))
	if e == nil {
		if e = json.Unmarshal(b, &cfg); e != nil {
			log.Fatal(e)
		}
	} else if !os.IsNotExist(e) {
		log.Fatal(e)
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *zone != "" {
		cfg.Timezone = *zone
	}
	serviceArgs := []string{"--data-dir", *dir}
	if *addr != "" {
		serviceArgs = append(serviceArgs, "--addr", *addr)
	}
	if *zone != "" {
		serviceArgs = append(serviceArgs, "--timezone", *zone)
	}
	d := &daemon{dir: *dir, cfg: cfg}
	svc, e := service.New(d, &service.Config{Name: "PlanMgrGo", DisplayName: "PlanMgr", Description: "PlanMgr scheduled task manager", Arguments: serviceArgs, WorkingDirectory: filepath.Dir(exe), Option: service.KeyValue{"RunWait": func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, stopSignals()...)
		defer signal.Stop(signals)
		select {
		case <-signals:
		case <-d.rt.done:
		}
	}}})
	if e != nil {
		log.Fatal(e)
	}
	if *serviceAction != "" {
		if *serviceAction == "status" {
			s, e := svc.Status()
			fmt.Println(s)
			if e != nil {
				log.Fatal(e)
			}
		} else if e = service.Control(svc, *serviceAction); e != nil {
			log.Fatal(e)
		}
		return
	}
	if !service.Interactive() {
		if e = svc.Run(); e != nil {
			log.Fatal(e)
		}
		return
	}
	rt, e := newRuntime(*dir, cfg, *noRunner)
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), stopSignals()...)
	defer cancel()
	select {
	case <-ctx.Done():
		rt.cancel()
		<-rt.done
	case <-rt.done:
	}
}
func importFromPHP(source, dest string) error {
	if e := os.MkdirAll(dest, 0700); e != nil {
		return e
	}
	target := filepath.Join(dest, "tasks.sqlite")
	if _, e := os.Stat(target); e == nil {
		return errors.New("目标数据库已存在，拒绝覆盖")
	}
	source, _ = filepath.Abs(source)
	dbpath := filepath.Join(source, "data", "tasks.sqlite")
	var ad Admin
	zone := "Asia/Shanghai"
	if b, e := os.ReadFile(filepath.Join(source, "config.php")); e == nil {
		get := func(key string) string {
			re := regexp.MustCompile(`'` + key + `'\s*=>\s*'([^']*)'`)
			m := re.FindSubmatch(b)
			if len(m) > 1 {
				return strings.ReplaceAll(strings.ReplaceAll(string(m[1]), `\\`, `\`), `\'`, `'`)
			}
			return ""
		}
		ad = Admin{get("username"), get("password_hash")}
		if p := get("database"); p != "" {
			dbpath = p
		}
		if z := get("timezone"); z != "" {
			zone = z
		}
	}
	// VACUUM INTO creates an online consistent snapshot without copying stale WAL files.
	src, e := sql.Open("sqlite", "file:"+filepath.ToSlash(dbpath)+"?mode=ro")
	if e != nil {
		return e
	}
	defer src.Close()
	if _, e = src.Exec("VACUUM INTO ?", target); e != nil {
		return e
	}
	db, e := openDB(target)
	if e != nil {
		return e
	}
	defer db.Close()
	if ad.Hash != "" {
		b, _ := json.Marshal(ad)
		if _, e = db.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES('administrator',?)", string(b)); e != nil {
			return e
		}
	}
	settings, _ := json.MarshalIndent(Settings{"127.0.0.1:8600", zone}, "", "  ")
	return os.WriteFile(filepath.Join(dest, "settings.json"), settings, 0600)
}
