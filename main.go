package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	dao "github.com/timothyclarke/http-request-broadcaster/dao"
)

const (
	maxIdleConnections int = 100
	requestTimeout     int = 5
)

var (
	locker    sync.RWMutex
	allCaches []dao.Cache

	groups  = make(map[string]dao.Group)
	clients = make(map[string]*http.Client)

	commandLine   = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	port          = commandLine.Int("port", 8088, "Broadcaster port.")
	httpsPort     = commandLine.Int("https-port", 8443, "Broadcaster https port.")
	grCount       = commandLine.Int("goroutines", 8, "Job handling goroutines pool. Higher is not implicitly better!")
	reqRetries    = commandLine.Int("retries", 1, "Request retry times against a cache - should the first attempt fail.")
	cachesCfgFile = commandLine.String("cfg", "/caches.ini", "Path pointing to the caches configuration file.")
	logFilePath   = commandLine.String("log-file", "", "Log file path.")
	enforceStatus = commandLine.Bool("enforce", false, "Enforces the status code of a request to be the first encountered non-200 received from a cache. Disabled by default.")
	enableLog     = commandLine.Bool("enable-log", false, "Switches logging on/off. Disabled by default.")
	crtFile       = commandLine.String("crt", "", "CRT file used for HTTPS support.")
	keyFile       = commandLine.String("key", "", "KEY file used for HTTPS support.")

	jobChannel = make(chan *Job, 2<<12)
	logChannel = make(chan []string, 2<<12)
	sigChannel = make(chan os.Signal, 1)
	hupChannel = make(chan os.Signal, 1)

	logBuffer bytes.Buffer
	logFile   *os.File

	defaultLocalAddr = net.IPAddr{IP: net.IPv4zero}
)

func createHTTPClient() *http.Client {
	d := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: defaultLocalAddr.IP, Zone: defaultLocalAddr.Zone},
		KeepAlive: 2 * time.Minute,
		Timeout:   30 * time.Second,
	}

	client := &http.Client{
		Transport: &http.Transport{
			DisableCompression:  true,
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConnsPerHost: maxIdleConnections,
			DisableKeepAlives:   false,
			Dial:                d.Dial,
		},
		Timeout: time.Duration(requestTimeout) * time.Second,
	}

	return client
}

type Job struct {
	Cache  dao.Cache
	Status chan int
	Result chan []byte
}

func newJob(cache dao.Cache) *Job {
	job := Job{}
	job.Cache = cache
	job.Result = make(chan []byte, 1)
	job.Status = make(chan int, 1)
	return &job
}

func hash(s string) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	return fmt.Sprintf("%v", h.Sum32())
}

func sendToLogChannel(args ...string) {
	if *enableLog {
		logChannel <- args
	}
}

// notifySigHup spawns a goroutine which will keep
// "listening" for hang-up signals. When such a signal
// occurs the configuration is reloaded from disk.
func notifySigHup() {
	signal.Notify(hupChannel, syscall.SIGHUP)

	go func() {
		for range hupChannel {
			sendToLogChannel("Sighup notification, reloading configuration.\n")

			err := readConfiguredCaches()
			if err != nil {
				fmt.Println(err.Error())
				os.Exit(1)
			}

			sendToLogChannel("Warming up connections.\n")

			err = setUpHttpClients()

			if err != nil {
				fmt.Println(err.Error())
				os.Exit(1)
			}
		}
	}()
}

// notifySigChannel waits for an Interrupt or Kill signal
// and gracefully handles it.
func notifySigChannel() {
	signal.Notify(sigChannel, os.Interrupt, os.Kill)

	go func(f *os.File) {
		<-sigChannel
		if *enableLog {
			if f != nil {
				f.Close()
			}
		}

		fmt.Println("Broadcaster exited succesfully.")
		os.Exit(0)
	}(logFile)
}

// startLog initializes and starts a goroutine that's going
// to listen the logChannel and write any entries that come along.
func startLog() error {

	var logWriter io.WriteCloser = os.Stdout

	if *logFilePath != "" {
		var logFileErr error
		logWriter, logFileErr = os.OpenFile(*logFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)

		if logFileErr != nil {
			return logFileErr
		}
	}

	go func(f io.WriteCloser) {
		for logEntry := range logChannel {
			logBuffer.Reset()
			logBuffer.WriteString(time.Now().Format(time.RFC3339))
			logBuffer.WriteString(" ")

			for _, logString := range logEntry {
				logBuffer.WriteString(logString)
			}

			io.WriteString(f, logBuffer.String())
		}
	}(logWriter)

	return nil
}

func doRequest(cache dao.Cache) (int, error) {
	locker.Lock()
	client := clients[cache.Name]
	locker.Unlock()

	reqString := cache.Address + cache.Item
	r, err := http.NewRequest(cache.Method, reqString, nil)

	if err != nil {
		return http.StatusInternalServerError, err
	}

	resp, err := client.Do(r)

	if err != nil {
		return http.StatusInternalServerError, err
	}

	_, err = io.Copy(ioutil.Discard, resp.Body)

	if err != nil {
		return http.StatusInternalServerError, err
	}

	resp.Body.Close()

	return resp.StatusCode, err

}

// jobWorker listens on the jobs channel and handles
// any incoming job.
func jobWorker(jobs <-chan *Job) {
	for job := range jobs {
		var out int
		var err error

		for i := 0; i <= *reqRetries; i++ {
			out, err = doRequest(job.Cache)
			if err == nil {
				break
			} else {
				// TODO: still need to decide what to do here.
				err = warmUpHttpClient(job.Cache)
				if err != nil {
					break
				}
			}
		}

		if err != nil {
			job.Result <- []byte(err.Error())
			continue
		}
		job.Status <- out
	}
}

// reqHandler handles any incoming http request. Its main purpose
// is to distribute the request further to all required caches.
func reqHandler(w http.ResponseWriter, r *http.Request) {

	var (
		groupName       string
		reqId           string
		broadcastCaches []dao.Cache
		reqStatusCode   = http.StatusOK
		respBody        = make(map[string]int)
	)

	for k, v := range r.Header {
		if strings.ToLower(k) == "x-group" {
			groupName = v[0]
			break
		}
	}

	if groupName == "" {
		broadcastCaches = allCaches
	} else {
		locker.Lock()
		if _, found := groups[groupName]; !found {
			var errText = fmt.Sprintf("Group %s not found.", groupName)
			sendToLogChannel(errText)
			http.Error(w, errText, http.StatusNotFound)
			locker.Unlock()
			return
		}
		broadcastCaches = groups[groupName].Caches
		locker.Unlock()
	}

	var cacheCount = len(broadcastCaches)

	if cacheCount == 0 {
		sendToLogChannel("Group ", groupName, " has no configured caches.")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var jobs = make([]*Job, cacheCount)

	for idx, bc := range broadcastCaches {
		bc.Method = r.Method
		bc.Item = r.URL.Path
		bc.Headers = r.Header

		job := newJob(bc)
		jobs[idx] = job
		jobChannel <- job
	}

	if *enableLog {
		reqId = hash(hash(time.Now().String()))
	}

	for _, job := range jobs {

		jobStatusCode := <-job.Status

		if *enforceStatus && reqStatusCode == http.StatusOK {
			reqStatusCode = jobStatusCode
		}

		respBody[job.Cache.Name] = jobStatusCode
		sendToLogChannel(reqId, " ", r.Method, " ", job.Cache.Address, r.URL.Path, " ", "\n")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(reqStatusCode)

	out, _ := json.MarshalIndent(respBody, "", "  ")
	w.Write(out)
}

func startBroadcastServer() {
	http.HandleFunc("/", reqHandler)

	if *crtFile != "" && *keyFile != "" {

		_, err := os.Stat(*crtFile)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		_, err = os.Stat(*keyFile)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "Broadcaster serving on %s...\n", strconv.Itoa(*httpsPort))
		fmt.Println(http.ListenAndServeTLS(":"+strconv.Itoa(*httpsPort), *crtFile, *keyFile, nil))

	} else {
		fmt.Fprintf(os.Stdout, "Broadcaster serving on %s...\n", strconv.Itoa(*port))
		fmt.Println(http.ListenAndServe(":"+strconv.Itoa(*port), nil))

	}
}

// setUpCaches reads the configured caches from the .ini file
// and populates a map having group name as key and slice of caches
// as values.
func readConfiguredCaches() error {
	locker.Lock()
	defer locker.Unlock()

	groupList, err := dao.LoadCachesFromIni(*cachesCfgFile)

	for _, g := range groupList {
		groups[g.Name] = g

		for _, cache := range g.Caches {
			_, err = url.Parse(cache.Address)

			if err != nil {
				return err
			}

			allCaches = append(allCaches, cache)
		}
	}

	return err
}

func warmUpHttpClient(cache dao.Cache) error {
	locker.Lock()
	client := createHTTPClient()

	clients[cache.Name] = client
	defer locker.Unlock()

	return nil
}

func setUpHttpClients() error {

	for _, cache := range allCaches {
		err := warmUpHttpClient(cache)
		if err != nil {
			return errors.New(fmt.Sprintf("* Cache [%s] encountered an error when warming up connections.\n    - %s\n", cache.Name, err.Error()))
		}
	}
	return nil
}

func main() {
	var err error

	runtime.GOMAXPROCS(runtime.NumCPU() - 1)

	commandLine.Usage = func() {
		fmt.Fprint(os.Stdout, "Usage of the broadcaster:\n")
		commandLine.PrintDefaults()
	}

	if err := commandLine.Parse(os.Args[1:]); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	if *enableLog {
		err = startLog()
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}

		defer logFile.Close()
	}

	if *cachesCfgFile == "" {
		fmt.Println("No configuration file specified. Use the -cfg parameter to specify one.")
		os.Exit(1)
	}

	fmt.Println("Loading configuration.")

	err = readConfiguredCaches()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	fmt.Println("Warming up connections.")

	err = setUpHttpClients()

	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	notifySigHup()
	notifySigChannel()

	for i := 0; i < (*grCount); i++ {
		go jobWorker(jobChannel)
	}

	startBroadcastServer()
}
