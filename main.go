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
	mu sync.Mutex

	validSet   sync.Map
	unknownSet sync.Map

	bufferMu      sync.Mutex
	validBuffer   []string
	unknownBuffer []string
)

const (
	inputFile   = "targets.txt"
	validFile   = "checkout.txt"
	unknownFile = "unknown.txt"
	checkDelay  = 30 * time.Second
)

func main() {
	fmt.Println("[START] Monitor de checkouts ultra pro (sin guardar dead)")

	for {
		targets, err := loadLines(inputFile)
		if err != nil {
			fmt.Println("Error leyendo targets:", err)
			time.Sleep(10 * time.Second)
			continue
		}

		var wg sync.WaitGroup
		for _, url := range targets {
			wg.Add(1)
			go func(u string) {
				defer wg.Done()
				check(u)
			}(url)
		}

		wg.Wait()
		flushBuffers()
		fmt.Println("[WAIT] siguiente ciclo...")
		time.Sleep(checkDelay)
	}
}

// ---------------- CHECK ----------------
func check(link string) {

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(link)
	if err != nil {
		// DEAD no se guarda
		fmt.Println("[DEAD]", link)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := strings.ToLower(string(bodyBytes))

	// -------- MÉTODO 1: Scripts de pago --------
	if strings.Contains(body, "js.stripe.com") || strings.Contains(body, "paypal.com/sdk/js") ||
		strings.Contains(body, "crypto") || strings.Contains(body, "bitcoin") || strings.Contains(body, "ethereum") {
		if _, ok := validSet.LoadOrStore(link, true); !ok {
			addToBuffer(&validBuffer, link)
			fmt.Println("[VALID]", link)
		}
		return
	}

	// -------- MÉTODO 2: Keywords débiles --------
	strongSignals := []string{
		"stripe",
		"paypal",
		"card",
		"credit",
		"visa",
		"mastercard",
		"bitcoin",
		"ethereum",
		"usdt",
	}

	weakSignals := []string{
		"checkout",
		"payment",
		"pago",
		"form",
		"input",
		"button",
	}

	strong := 0
	weak := 0

	for _, s := range strongSignals {
		if strings.Contains(body, s) {
			strong++
		}
	}
	for _, s := range weakSignals {
		if strings.Contains(body, s) {
			weak++
		}
	}

	if resp.StatusCode == 200 && (strong >= 1 || weak >= 3) {
		if _, ok := validSet.LoadOrStore(link, true); !ok {
			addToBuffer(&validBuffer, link)
			fmt.Println("[VALID]", link)
		}
		return
	}

	// -------- UNKNOWN --------
	if resp.StatusCode == 200 {
		if _, ok := unknownSet.LoadOrStore(link, true); !ok {
			addToBuffer(&unknownBuffer, link)
			fmt.Println("[UNKNOWN]", link)
		}
		return
	}

	// DEAD no se guarda
	fmt.Println("[DEAD]", link)
}

// ---------------- HELPERS ----------------
func addToBuffer(buffer *[]string, text string) {
	bufferMu.Lock()
	defer bufferMu.Unlock()
	*buffer = append(*buffer, text)
}

// cada ciclo flush de buffers a archivo
func flushBuffers() {
	bufferMu.Lock()
	defer bufferMu.Unlock()

	if len(validBuffer) > 0 {
		save(validFile, validBuffer)
		validBuffer = nil
	}

	if len(unknownBuffer) > 0 {
		save(unknownFile, unknownBuffer)
		unknownBuffer = nil
	}
}

// ---------------- FILE ----------------
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

func save(file string, lines []string) {
	mu.Lock()
	defer mu.Unlock()

	f, _ := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer f.Close()
	for _, line := range lines {
		f.WriteString(line + "\n")
	}
}
