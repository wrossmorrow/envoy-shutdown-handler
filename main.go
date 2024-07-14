package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CLI args and globals

var (
	port         = flag.String("shutdown-handler-port", "9001", "port to listen on")
	host         = flag.String("envoy-admin-host", "localhost", "envoy admin interface host")
	admin        = flag.String("envoy-admin-port", "9901", "envoy admin interface port")
	scheme       = flag.String("envoy-admin-scheme", "http", "envoy admin interface HTTP/S scheme")
	delay        = flag.Int("initial-delay-seconds", 0, "delay in seconds before starting shutdown")
	period       = flag.Int("check-period-seconds", 5, "period in seconds to pause while checking for active connections")
	deadline     = flag.Int("check-deadline-seconds", 300, "deadline in seconds to wait for active connections to close")
	force        = flag.Bool("force", false, "force shutdown when active connections are drained")
	statsRegex   = regexp.MustCompile("http[.]envoy[.]downstream_cx_active:[ ]+([0-9]+)")
	complete     chan bool // make(chan bool, 1)
	adminBaseUrl string
)

// Helper methods

func failEnvoyHealthCheck() error {
	resp, err := http.Post(adminBaseUrl+"/healthcheck/fail", "text/plain", nil)
	if err != nil {
		return fmt.Errorf("failed to send shutdown request to envoy admin interface: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send shutdown request to envoy admin interface: %v", resp.Status)
	}
	return nil
}

func countDownstreamCnx() (int, error) {
	resp, err := http.Get(adminBaseUrl + "/stats?filter=http.envoy.downstream_cx_active")
	if err != nil {
		log.Printf("Failed to get envoy stats: %v", err)
		return -1, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("failed to get envoy stats: %v", resp.Status)
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1, err
	}
	bodyString := strings.TrimSpace(string(bodyBytes))
	count, err := parseDownstreamCnx(bodyString)
	if err != nil {
		log.Printf("Failed to query or parse envoy stats: %v", err)
	} else {
		log.Printf("Received envoy stats: %v downstream connections open", count)
	}
	return count, err
}

func parseDownstreamCnx(body string) (int, error) {
	matches := statsRegex.FindStringSubmatch(body)
	if len(matches) != 2 {
		return -1, fmt.Errorf("failed to parse envoy downstream connections from string: \"%v\"", body)
	}
	return strconv.Atoi(matches[1])
}

func startGracefulDraining() error {
	resp, err := http.Post(adminBaseUrl+"/drain_listeners?graceful", "text/plain", nil)
	if err != nil {
		return fmt.Errorf("failed to start graceful draining: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to start graceful draining: %v", resp.Status)
	}
	return nil
}

func forceEnvoyShutdown() error {
	resp, err := http.Post(adminBaseUrl+"/quitquitquit", "text/plain", nil)
	if err != nil {
		return fmt.Errorf("failed to send force shutdown request to envoy admin interface: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to send force shutdown request to envoy admin interface: %v", resp.Status)
	}
	return nil
}

func defaultIntFromQuery(r *http.Request, key string, def int) (int, error) {
	if vals, ok := r.URL.Query()[key]; ok {
		if vals[0] != "" {
			val, err := strconv.Atoi(vals[0])
			if err == nil {
				return val, nil
			}
			return def, err
		}
	}
	return def, nil
}

func defaultNonNegIntFromQuery(r *http.Request, key string, def int) (int, error) {
	val, err := defaultIntFromQuery(r, key, def)
	if err != nil {
		return val, err
	}
	if val < 0 {
		return def, fmt.Errorf("invalid negative value for %v: %d", key, val)
	}
	return val, nil
}

func parseCommonQueryParams(r *http.Request) (int, int, int, error) {

	qDelay, err := defaultNonNegIntFromQuery(r, "delay", *delay)
	if err != nil {
		return -1, -1, -1, err
	}

	qPeriod, err := defaultNonNegIntFromQuery(r, "period", *period)
	if err != nil {
		return -1, -1, -1, err
	}

	qDeadline, err := defaultNonNegIntFromQuery(r, "deadline", *deadline)
	if err != nil {
		return -1, -1, -1, err
	}

	return qDelay, qPeriod, qDeadline, nil

}

// HTTP handlers

func alive(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func ready(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func checkStats(w http.ResponseWriter, r *http.Request) {
	log.Print("Stats check request received")
	c, err := countDownstreamCnx()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("error getting downstream connections: %v\n", err)))
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("http.envoy.downstream_cx_active: %v\n", c)))
}

func shutdown(w http.ResponseWriter, r *http.Request) {

	var err error

	log.Print("Shutdown request received")

	qDelay, qPeriod, qDeadline, err := parseCommonQueryParams(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("error: %v\n", err)))
		return
	}

	if qDeadline < qDelay+qPeriod {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("error: deadline (%d) too short relative to delay (%d) and period (%d)\n", qDeadline, qDelay, qPeriod)))
		return
	}

	started := time.Now()
	complete = make(chan bool, 1)

	// tell envoy to fail it's healthchecks first
	err = failEnvoyHealthCheck()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("error: %v\n", err)))
		complete <- false
		return
	}

	// delay if asked, to allow for healthcheck failures to be observed
	if qDelay > 0 {
		log.Printf("Delaying graceful shutdown by %v seconds", qDelay)
		time.Sleep(time.Duration(qDelay) * time.Second)
	}

	// check for active connections, if there are none immediate shutdown is safe
	c, err := countDownstreamCnx()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("error: %v\n", err)))
		complete <- false
		return
	}
	if c == 0 {
		log.Print("No active downstream connections, can shut down")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("No active downstream connections, can shut down"))
		complete <- true
		return
	}

	// there were active connections, so start graceful draining
	err = startGracefulDraining()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf("error: %v\n", err)))
		complete <- false
		return
	}

	log.Printf("Waiting for %v active downstream connections to close", c)
	for {
		c, err := countDownstreamCnx()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf("error: %v\n", err)))
			complete <- false
			return
		}
		if c == 0 {
			log.Print("All downstream connections closed, can shut down")
			break
		}
		if time.Since(started) > time.Duration(qDeadline)*time.Second {
			log.Print("Timeout waiting for downstream connections to close")
			w.WriteHeader(http.StatusRequestTimeout)
			w.Write([]byte("Timeout waiting for downstream connections to close\n"))
			complete <- true
			return
		}
		time.Sleep(time.Duration(qPeriod) * time.Second)
	}

	// shut envoy down now explicitly if "force" flag is set
	if *force {
		err = forceEnvoyShutdown()
		if err != nil {
			log.Printf("Failed to force envoy shutdown: %v", err)
		}
	}

	// return to allow SIGTERMs to be sent to the containers
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("All downstream connections closed, can shut down\n"))

	// signal that the shutdown is complete to any channel listeners
	complete <- true
}

func waitForShutdown(w http.ResponseWriter, r *http.Request) {

	log.Print("Waiting for shutdown")

	qDelay, qPeriod, qDeadline, err := parseCommonQueryParams(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("error: %v\n", err)))
		return
	}

	if qDeadline < qDelay+qPeriod {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf("error: deadline (%d) too short relative to delay (%d) and period (%d)\n", qDeadline, qDelay, qPeriod)))
		return
	}

	started := time.Now()

	// delay if asked, to allow for healthcheck failures to be observed
	if qDelay > 0 {
		log.Printf("Delaying wait for shutdown by %v seconds", qDelay)
		time.Sleep(time.Duration(qDelay) * time.Second)
	}

	for complete == nil {
		time.Sleep(time.Duration(qPeriod) * time.Second)
		if time.Since(started) > time.Duration(qDeadline)*time.Second {
			log.Print("Timeout waiting for shutdown to complete")
			w.WriteHeader(http.StatusRequestTimeout)
			w.Write([]byte("Timeout waiting for shutdown to complete\n"))
			return
		}
	}

	success := <-complete
	if success {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("shutdown completed\n"))
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("an error occurred during shutdown\n"))
	}

	complete = nil
}

// Runtime (no signal handling)

func main() {
	flag.Parse()
	adminBaseUrl = *scheme + "://" + *host + ":" + *admin
	log.Print("Running envoy shutdown handler server on " + *port)
	http.HandleFunc("/health/alive", alive)
	http.HandleFunc("/health/ready", ready)
	http.HandleFunc("/check/stats", checkStats)
	http.HandleFunc("/shutdown", shutdown)
	http.HandleFunc("/waitforshutdown", waitForShutdown)
	err := http.ListenAndServe(":"+*port, nil)
	log.Printf("%v", err)
}
