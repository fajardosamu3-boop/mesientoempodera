package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
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
	fmt.Println("[START] Monitor ultra pro con Headless Chrome")

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

	// Create context with timeout
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var htmlContent string

	err := chromedp.Run(ctx,
		chromedp.Navigate(link),
		chromedp.Sleep(2*time.Second), // espera a que cargue JS
		chromedp.OuterHTML("html", &htmlContent, chromedp.NodeVisible),
	)
	if err != nil {
		fmt.Println("[DEAD]", link)
		return
	}

	body := strings.ToLower(htmlContent)

	// -------- VALID si scripts de pago detectados --------
	paymentSignals := []string{
		"js.stripe.com",
		"paypal.com/sdk/js",
		"crypto",
		"bitcoin",
		"ethereum",
		"usdt",
		"card",
		"checkout-button",
	}

	for _, s := range paymentSignals {
		if strings.Contains(body, s) {
			if _, ok := validSet.LoadOrStore(link, true); !ok {
				addToBuffer(&validBuffer, link)
				fmt.Println("[VALID]", link)
			}
			return
		}
	}

	// -------- UNKNOWN si HTML cargó pero sin señales --------
	if _, ok := unknownSet.LoadOrStore(link, true); !ok {
		addToBuffer(&unknownBuffer, link)
		fmt.Println("[UNKNOWN]", link)
	}
}

// ---------------- HELPERS ----------------
func addToBuffer(buffer *[]string, text string) {
	bufferMu.Lock()
	defer bufferMu.Unlock()
	*buffer = append(*buffer, text)
}

// flush de buffers a archivo
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
