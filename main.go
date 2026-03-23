package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	validCount   int
	invalidCount int
	retryCount   int

	mu sync.Mutex

	seen sync.Map

	bufferMu    sync.Mutex
	validBuffer []string

	outputFile   = "checkout.txt"
	inputTargets = "targets.txt"
)

// ---------------- MAIN ----------------
func main() {
	targets, err := loadLines(inputTargets)
	if err != nil || len(targets) == 0 {
		fmt.Println("No hay targets en targets.txt")
		return
	}

	jobs := make(chan string, 100)

	var wg sync.WaitGroup

	workers := 10
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go worker(jobs, &wg)
	}

	go flusher()
	go stats()

	for _, t := range targets {
		if _, loaded := seen.LoadOrStore(t, true); !loaded {
			jobs <- t
		}
	}

	wg.Wait()
}

// ---------------- WORKER ----------------
func worker(jobs chan string, wg *sync.WaitGroup) {
	defer wg.Done()

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	for url := range jobs {

		status := check(client, url)

		switch status {
		case "VALID":
			addToBuffer(url)

		case "RETRY":
			time.Sleep(2 * time.Second)
			jobs <- url
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// ---------------- CHECK ----------------
func check(client *http.Client, url string) string {

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log("INVALID", url)
		return "INVALID"
	}

	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		log("RETRY", url)
		return "RETRY"
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		log("RETRY", url)
		return "RETRY"
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := strings.ToLower(string(bodyBytes))

	// 🔥 detección avanzada de pagos
	keywords := []string{
		"checkout",
		"payment",
		"pago",
		"stripe",
		"paypal",
		"visa",
		"mastercard",
		"card",
		"credit card",
		"crypto",
		"bitcoin",
		"ethereum",
		"usdt",
		"wallet",
		"€",
		"$",
	}

	matches := 0
	for _, kw := range keywords {
		if strings.Contains(body, kw) {
			matches++
		}
	}

	if resp.StatusCode == 200 && matches >= 2 {
		log("VALID", url)
		return "VALID"
	}

	log("INVALID", url)
	return "INVALID"
}

// ---------------- BUFFER ----------------
func addToBuffer(link string) {
	bufferMu.Lock()
	defer bufferMu.Unlock()
	validBuffer = append(validBuffer, link)
}

// cada 30s guarda en checkout.txt
func flusher() {
	for {
		time.Sleep(30 * time.Second)

		bufferMu.Lock()
		if len(validBuffer) == 0 {
			bufferMu.Unlock()
			continue
		}

		f, _ := os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		for _, link := range validBuffer {
			f.WriteString(link + "\n")
		}
		f.Close()

		fmt.Println("[FLUSH] Guardados:", len(validBuffer))

		validBuffer = nil
		bufferMu.Unlock()
	}
}

// ---------------- UTIL ----------------
func loadLines(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// ---------------- LOG ----------------
func log(status, url string) {
	mu.Lock()
	defer mu.Unlock()

	switch status {
	case "VALID":
		validCount++
		fmt.Println("[VALID]", url)
	case "INVALID":
		invalidCount++
	case "RETRY":
		retryCount++
	}
}

// ---------------- STATS ----------------
func stats() {
	for {
		mu.Lock()
		fmt.Printf("[STATS] V:%d I:%d R:%d\n",
			validCount, invalidCount, retryCount)
		mu.Unlock()

		time.Sleep(5 * time.Second)
	}
}
